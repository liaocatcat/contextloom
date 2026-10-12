package runtime

import (
	"context"
	"fmt"
	"sync"

	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/modelgateway"
	"github.com/contextloom/contextloom-agent/internal/orchestrator"
)

// ── 预设→后端解析（员工/子 Agent 自选模型的核心链路）──
//
// 设计动机：Employee.Model.Preset 此前仅作为 modelgateway.Req.Role 透传给
// 网关——只有当 gateway.rules 里恰好存在 role=<预设名> 规则时才生效，否则
// 是静默空操作（子 Agent 仍走默认后端）。本文件提供【直接解析】路径：
// 按预设名从 config.models 找到预设 → model.NewBackend 建实例 → 注入参数
// （top_p/temperature/supports_vision），不依赖网关配置即可生效。
//
// 优先级（resolveSubAgentBackend）：
//  1. 显式 override（spawn_task model 参数 / 用户指派）——最高，自选模型；
//  2. 员工档案 model.preset + model.fallbacks——多来源组合（ChainBackend）；
//  3. 网关路由（Req.Role=员工名，命中 role=<员工名> 规则时生效）；
//  4. rt.Backend（兜底，旧行为零破坏）。
//
// 实例缓存：同一预设只建厂一次（进程内复用，per-Runtime——不同 Runtime 的
// 配置不同，包级缓存会串配置）。applyLiveConfig 后由 InvalidateModelCache
// 失效——预设热更新后下次解析重建实例。

type presetCacheEntry struct {
	backend model.Backend
	err     error
}

// presetCacheMu 保护 Runtime.presetCache（map 惰性创建）。
var presetCacheMu sync.Mutex

// InvalidateModelCache 清空预设→后端实例缓存（applyLiveConfig 后调用）。
func (rt *Runtime) InvalidateModelCache() {
	presetCacheMu.Lock()
	rt.presetCache = nil
	presetCacheMu.Unlock()
}

// buildBackendForPreset 按预设名解析为后端实例（带缓存）。预设不存在或建厂
// 失败返回 error（调用方降级下一来源/网关/默认后端）。
func (rt *Runtime) buildBackendForPreset(name string) (model.Backend, error) {
	name = trimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("预设名为空")
	}
	presetCacheMu.Lock()
	if e, ok := rt.presetCache[name]; ok {
		presetCacheMu.Unlock()
		return e.backend, e.err
	}
	presetCacheMu.Unlock()

	backend, err := rt.buildBackendForPresetUncached(name)
	presetCacheMu.Lock()
	if rt.presetCache == nil {
		rt.presetCache = make(map[string]presetCacheEntry)
	}
	rt.presetCache[name] = presetCacheEntry{backend: backend, err: err}
	presetCacheMu.Unlock()
	return backend, err
}

// buildBackendForPresetUncached 预设解析（无缓存）：查预设 → 建厂 → 注入参数。
func (rt *Runtime) buildBackendForPresetUncached(name string) (model.Backend, error) {
	if rt.Config == nil {
		return nil, fmt.Errorf("配置未加载")
	}
	preset := rt.findModelPreset(name)
	if preset == nil {
		avail := rt.ModelNames()
		if len(avail) == 0 {
			return nil, fmt.Errorf("未找到模型预设 %q（配置中无任何模型预设）", name)
		}
		return nil, fmt.Errorf("未找到模型预设 %q，可用: %s", name, joinStrings(avail, " / "))
	}
	sendEffort := true
	if preset.SendEffort != nil {
		sendEffort = *preset.SendEffort
	}
	backend, err := model.NewBackend(preset.Backend, preset.Endpoint, preset.APIKey, preset.Backend, preset.MaxContext, sendEffort)
	if err != nil {
		return nil, fmt.Errorf("为预设 %q 建后端失败: %w", preset.Name, err)
	}
	if preset.TopP != nil {
		backend.SetTopP(*preset.TopP)
	}
	if preset.Temperature != nil {
		backend.SetTemperature(*preset.Temperature)
	}
	if ds, ok := backend.(*model.DeepSeekBackend); ok {
		ds.SetSupportsVision(preset.SupportsVision)
	}
	return backend, nil
}

// employeeChainBackend 把员工档案的多来源绑定解析为组合后端。
// 全部来源解析失败/无绑定 → (nil, nil)（调用方继续网关/默认路径）。
func (rt *Runtime) employeeChainBackend(emp *orchestrator.Employee) (model.Backend, error) {
	if emp == nil {
		return nil, nil
	}
	policy, err := emp.ResolvePolicy()
	if err != nil || (policy.ModelPreset == "" && len(policy.ModelFallbacks) == 0) {
		return nil, nil
	}
	var sources []model.Backend
	var firstErr error
	for _, name := range append([]string{policy.ModelPreset}, policy.ModelFallbacks...) {
		if trimSpace(name) == "" {
			continue
		}
		b, err := rt.buildBackendForPreset(name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sources = append(sources, b)
	}
	if len(sources) == 0 {
		if firstErr != nil {
			return nil, firstErr
		}
		return nil, nil
	}
	chain := model.NewChainBackend(sources...)
	if chain == nil {
		return nil, nil
	}
	return chain, nil
}

// resolveSubAgentBackend 子 Agent 后端解析（自选模型 + 员工绑定 + 网关 + 默认）。
// 优先级见文件头注释。返回的后端保证非 nil（最终兜底 rt.Backend）。
func (rt *Runtime) resolveSubAgentBackend(ctx context.Context, override string, emp *orchestrator.Employee, req modelgateway.Req) model.Backend {
	// 1) 显式自选模型（spawn_task model 参数等）：仅接受存在的预设
	if name := trimSpace(override); name != "" {
		if b, err := rt.buildBackendForPreset(name); err == nil {
			return b
		}
		// 预设不存在：降级员工/网关路径（不硬失败，避免一次错名阻断子任务）
	}
	// 2) 员工档案绑定：主来源 + 备选来源组合成降级链（多来源搭配）
	if chain, err := rt.employeeChainBackend(emp); err == nil && chain != nil {
		return chain
	}
	// 3) 网关路由（req.Role=员工名，命中 role=<员工名> 规则时生效）
	if b := rt.selectBackend(ctx, req); b != nil && b != rt.Backend {
		return b
	}
	// 4) 默认后端（零配置兜底）
	if rt.Backend != nil {
		return rt.Backend
	}
	// 极端兜底：selectBackend 理论上不返回 nil，防御
	return rt.selectBackend(ctx, req)
}

// trimSpace 去空白（避免每个调用点 import strings 的循环噪音）。
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// joinStrings 连接字符串（strings.Join 的包内别名）。
func joinStrings(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
