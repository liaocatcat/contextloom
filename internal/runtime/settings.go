package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// saveSettings 持久化设置到项目目录 .contextloom/settings.json
func (rt *Runtime) saveSettings() {
	// modeBySID 快照（historyMu 保护；SetMode 等并发写入场景防 data race）
	rt.historyMu.RLock()
	modes := make(map[string]string, len(rt.modeBySID))
	for k, v := range rt.modeBySID {
		modes[k] = v
	}
	rt.historyMu.RUnlock()
	dir := filepath.Join(rt.Config.Project.Root, ".contextloom")
	os.MkdirAll(dir, 0755)
	settings := map[string]any{
		"effort":             rt.Config.CLIDefaults.Effort,
		"mode":               rt.Config.CLIDefaults.Mode,
		"modes":              modes, // 按会话隔离的模式（问题 2：切换模式不影响其他会话）
		"score_threshold":    rt.Config.Harness.ScoreThreshold,
		"overseer_threshold": rt.Config.Overseer.PassThreshold,
		"active_skills":      rt.activeSkills,
		"top_p":              rt.topPOr(0.95),
		"temperature":        rt.temperatureOr(1.0),
		// /model 切换与 GUI 模型预设切换均在此持久化激活模型名，
		// 重启后 loadSettings 恢复，避免"切换不持久化"（此前缺失导致 /model 无效）。
		"active_model": rt.Config.ActiveModelName,
	}
	data, _ := json.MarshalIndent(settings, "", "  ")
	os.WriteFile(filepath.Join(dir, "settings.json"), data, 0644)
}

// loadSettings 从项目目录加载持久化设置
func (rt *Runtime) loadSettings() {
	data, err := os.ReadFile(filepath.Join(rt.Config.Project.Root, ".contextloom", "settings.json"))
	if err != nil {
		return
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		return
	}
	if effort, ok := s["effort"].(string); ok {
		rt.Config.CLIDefaults.Effort = effort
		rt.Backend.SetReasoningEffort(effort)
	}
	if mode, ok := s["mode"].(string); ok && (mode == "chat" || mode == "task" || mode == "goal") {
		rt.Config.CLIDefaults.Mode = mode
	}
	// 恢复按会话隔离的模式表（modes: {会话UUID: mode}；仅接受合法值）
	if modes, ok := s["modes"].(map[string]any); ok && rt.modeBySID != nil {
		for k, v := range modes {
			if ms, ok := v.(string); ok && (ms == "chat" || ms == "task" || ms == "goal") {
				rt.modeBySID[k] = ms
			}
		}
	}
	if thr, ok := s["score_threshold"].(float64); ok {
		rt.Config.Harness.ScoreThreshold = thr
		rt.Harness.SetThreshold(types.Score(thr))
	}
	if othr, ok := s["overseer_threshold"].(float64); ok {
		rt.Config.Overseer.PassThreshold = othr
	}
	// 恢复上次选中的技能列表
	if skills, ok := s["active_skills"].([]any); ok {
		rt.activeSkills = make([]string, 0, len(skills))
		for _, v := range skills {
			if name, ok := v.(string); ok {
				rt.activeSkills = append(rt.activeSkills, name)
			}
		}
	}
	if tp, ok := s["top_p"].(float64); ok {
		v := tp
		rt.UpdateActivePreset(func(m *config.ModelPreset) { m.TopP = &v })
		rt.Backend.SetTopP(tp)
	}
	if temp, ok := s["temperature"].(float64); ok {
		v := temp
		rt.UpdateActivePreset(func(m *config.ModelPreset) { m.Temperature = &v })
		rt.Backend.SetTemperature(temp)
	}
	// 恢复上次 /model 切换的激活模型名（校验存在于预设列表，防配置漂移指向不存在模型）。
	// 注意：后端重建由 runtime.New 在 ctxBudget 初始化后据 prevModel 对比触发（applyLiveConfig），
	// 此处仅恢复配置指针，避免在 Backend 就绪前重建。
	if am, ok := s["active_model"].(string); ok && am != "" {
		if rt.findModelPreset(am) != nil {
			rt.Config.ActiveModelName = am
		}
	}
}

// loadActiveSkills 兼容方法（实际恢复在 loadSettings 中完成）
func (rt *Runtime) loadActiveSkills() {}

func (rt *Runtime) SaveSettings() {
	rt.saveSettings()
}

// GetCurrentSessionID 返回当前会话 ID（由 NewSession 或 LoadSession 设置）
func (rt *Runtime) GetCurrentSessionID() string {
	if rt == nil {
		return ""
	}
	rt.historyMu.RLock()
	defer rt.historyMu.RUnlock()
	return rt.currentSessionID
}

// TopP 返回当前核采样参数
func (rt *Runtime) TopP() float64 {
	if rt == nil || rt.Backend == nil {
		return 0.95
	}
	return rt.Backend.TopP()
}

// Temperature 返回当前温度参数
func (rt *Runtime) Temperature() float64 {
	if rt == nil || rt.Backend == nil {
		return 1.0
	}
	return rt.Backend.Temperature()
}

// topPOr 返回 TopP 值，nil 时使用默认值
func (rt *Runtime) topPOr(def float64) float64 {
	if v := rt.Config.ActiveModel().TopP; v != nil {
		return *v
	}
	return def
}

// temperatureOr 返回 Temperature 值，nil 时使用默认值
func (rt *Runtime) temperatureOr(def float64) float64 {
	if v := rt.Config.ActiveModel().Temperature; v != nil {
		return *v
	}
	return def
}
