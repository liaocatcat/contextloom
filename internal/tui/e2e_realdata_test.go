package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
)

// TestE2E_RealData_LoadHistory 用真实项目 .contextloom/memory 的数据副本，
// 模拟 contextloom --memory-load <uuid> 启动后 TUI 能否把历史对话加载到界面消息列表。
// 这是用户上报场景的端到端复现：跨进程持久化的历史事件 → LoadSession → initialModel。
func TestE2E_RealData_LoadHistory(t *testing.T) {
	// 1) 准备数据副本（不污染真实数据）
	// 测试 cwd = 包目录 internal/tui/，向上两级到项目根。
	// 注意：.contextloom/ 属于运行时数据，被 .gitignore 排除、不随仓库分发；
	// 本测试依赖本机真实数据，干净克隆/CI 环境无此目录时自动跳过。
	src := "../../.contextloom/memory"
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		t.Skipf("本机无真实运行数据(%s)，跳过本地 E2E 测试: %v", src, err)
	}
	tmp, err := os.MkdirTemp("", "contextloom-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	if err := copyDir(src, tmp+"/memory"); err != nil {
		t.Fatalf("拷贝真实数据失败: %v", err)
	}

	// 2) 用与 main.go runTUI 相同的配置构造 runtime
	cfg := &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmp + "/memory", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmp + "/plugins"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "chat"},
		Overseer:        config.OverseerConfig{PassThreshold: 90},
		Project:         config.ProjectConfig{Root: tmp, Vocab: tmp + "/vocab.json"},
	}
	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// 3) 模拟 main.go --memory-load：LoadSession 恢复 currentSessionID
	//    取本机真实数据（.contextloom/memory/sessions/）中的首个会话
	entries, err := os.ReadDir(src + "/sessions")
	if err != nil {
		t.Fatalf("读取示例数据目录失败: %v", err)
	}
	var uuid string
	for _, e := range entries {
		if name := e.Name(); strings.HasSuffix(name, ".jsonl") {
			uuid = strings.TrimSuffix(name, ".jsonl")
			break
		}
	}
	if uuid == "" {
		t.Skip("无示例会话数据")
	}
	// 从示例会话 JSONL 中提取首条用户消息片段，作为断言依据（不写死会话内容）
	raw, err := os.ReadFile(filepath.Join(src, "sessions", uuid+".jsonl"))
	if err != nil {
		t.Fatalf("读取示例事件日志失败: %v", err)
	}
	firstUser := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, `"user_msg"`) {
			continue
		}
		var evt struct {
			Data string `json:"data"`
		}
		if json.Unmarshal([]byte(line), &evt) == nil && evt.Data != "" {
			firstUser = evt.Data
			break
		}
	}
	if firstUser == "" {
		t.Skip("示例会话无用户消息")
	}
	if _, _, err := rt.LoadSession(uuid); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	// 4) 启动 TUI（main.go runTUI 路径）
	m := initialModel(rt)
	if m.currentSessionID != uuid {
		t.Fatalf("currentSessionID=%q want %q", m.currentSessionID, uuid)
	}
	var joined strings.Builder
	for _, msg := range m.messages {
		joined.WriteString(msg.Content)
		joined.WriteString("\n")
	}
	out := joined.String()
	// 首轮用户消息与助手回复均应加载到界面
	if !strings.Contains(out, firstUser) {
		t.Errorf("历史用户消息未加载到界面，实际 messages:\n%s", out)
	}
	if !strings.Contains(out, "处理结果") {
		t.Errorf("历史助手回复未加载到界面，实际 messages:\n%s", out)
	}
	t.Logf("加载到 %d 条历史消息", len(m.messages))
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	})
}
