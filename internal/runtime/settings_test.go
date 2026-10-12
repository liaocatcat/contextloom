package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/harness"
)

// TestLoadSettings_ModeGoal 验证 settings.json 中的 goal 模式能被正确加载
// （回归：加载白名单原先只有 chat/task，goal 被丢弃 → 重启回到 chat）。
func TestLoadSettings_ModeGoal(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".contextloom"), 0755)
	data := `{"mode": "goal", "effort": "max"}`
	if err := os.WriteFile(filepath.Join(dir, ".contextloom", "settings.json"), []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	rt := &Runtime{Config: &config.Config{}}
	rt.Config.Project.Root = dir
	rt.Config.CLIDefaults.Mode = "chat" // 默认 chat
	rt.Backend = &mockBackend{}
	rt.loadSettings()

	if rt.GetMode() != "goal" {
		t.Fatalf("期望从 settings.json 恢复 goal 模式，实际 %q", rt.GetMode())
	}
}

// TestLoadSettings_ModeChatAndTask 验证 chat/task 仍正常加载（不回归）。
func TestLoadSettings_ModeChatAndTask(t *testing.T) {
	for _, want := range []string{"chat", "task"} {
		dir := t.TempDir()
		os.MkdirAll(filepath.Join(dir, ".contextloom"), 0755)
		data := `{"mode": "` + want + `"}`
		if err := os.WriteFile(filepath.Join(dir, ".contextloom", "settings.json"), []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		rt := &Runtime{Config: &config.Config{}}
		rt.Config.Project.Root = dir
		rt.Config.CLIDefaults.Mode = "chat"
		rt.Backend = &mockBackend{}
		rt.loadSettings()
		if rt.GetMode() != want {
			t.Fatalf("期望加载 %q，实际 %q", want, rt.GetMode())
		}
	}
}

// TestSaveAndLoadSettings_RoundTrip 保存后重新加载保持一致（含 goal 模式）。
func TestSaveAndLoadSettings_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	rt := &Runtime{Config: &config.Config{}}
	rt.Config.Project.Root = dir
	rt.Config.CLIDefaults.Mode = "goal"
	rt.Config.CLIDefaults.Effort = "max"
	rt.Config.Harness.ScoreThreshold = 50
	rt.Config.Overseer.PassThreshold = 90
	rt.Harness = harness.New(harness.Config{ScoreThreshold: 50})
	rt.saveSettings()

	rt2 := &Runtime{Config: &config.Config{}}
	rt2.Config.Project.Root = dir
	rt2.Config.CLIDefaults.Mode = "chat"
	rt2.Backend = &mockBackend{}
	rt2.Harness = harness.New(harness.Config{ScoreThreshold: 50})
	rt2.loadSettings()
	if rt2.GetMode() != "goal" {
		t.Fatalf("保存-加载往返后期望 goal，实际 %q", rt2.GetMode())
	}
}

// TestSaveSettings_PersistsActiveModel：/model 切换必须持久化 active_model 到 settings.json
// （此前缺失导致重启后模型切换丢失——用户报告的"/model 不能持久化"根因）。
func TestSaveSettings_PersistsActiveModel(t *testing.T) {
	dir := t.TempDir()
	rt := &Runtime{Config: &config.Config{}}
	rt.Config.Project.Root = dir
	rt.Config.ActiveModelName = "gpt-4o"
	rt.Harness = harness.New(harness.Config{})
	rt.saveSettings()

	data, err := os.ReadFile(filepath.Join(dir, ".contextloom", "settings.json"))
	if err != nil {
		t.Fatalf("读取 settings.json 失败: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("解析 settings.json 失败: %v", err)
	}
	if got, _ := s["active_model"].(string); got != "gpt-4o" {
		t.Fatalf("settings.json 期望 active_model=gpt-4o，实际 %q（/model 切换未持久化）", got)
	}
}

// TestLoadSettings_RestoresActiveModel：重启后从 settings.json 恢复上次切换的激活模型。
// 无效模型名（不在预设列表）必须忽略，防止配置漂移。
func TestLoadSettings_RestoresActiveModel(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".contextloom"), 0755)
	data, _ := json.MarshalIndent(map[string]any{"active_model": "qwen"}, "", "  ")
	os.WriteFile(filepath.Join(dir, ".contextloom", "settings.json"), data, 0644)

	rt := &Runtime{Config: &config.Config{}}
	rt.Config.Project.Root = dir
	rt.Config.Models = []config.ModelPreset{
		{Name: "default-model", Backend: "deepseek", MaxContext: 131072},
		{Name: "qwen", Backend: "openai", Endpoint: "https://dashscope.aliyuncs.com/compatible-mode/v1", MaxContext: 128000},
	}
	rt.Config.ActiveModelName = "default-model"
	rt.Backend = &mockBackend{}
	rt.Harness = harness.New(harness.Config{})
	rt.loadSettings()
	if rt.Config.ActiveModelName != "qwen" {
		t.Fatalf("loadSettings 后期望恢复 qwen，实际 %q", rt.Config.ActiveModelName)
	}

	// 无效模型名必须被忽略（保持原值）
	os.WriteFile(filepath.Join(dir, ".contextloom", "settings.json"), []byte(`{"active_model":"no-such-model"}`), 0644)
	rt2 := &Runtime{Config: &config.Config{}}
	rt2.Config.Project.Root = dir
	rt2.Config.Models = rt.Config.Models
	rt2.Config.ActiveModelName = "default-model"
	rt2.Backend = &mockBackend{}
	rt2.Harness = harness.New(harness.Config{})
	rt2.loadSettings()
	if rt2.Config.ActiveModelName != "default-model" {
		t.Fatalf("无效模型名应被忽略，实际被改成 %q", rt2.Config.ActiveModelName)
	}
}
