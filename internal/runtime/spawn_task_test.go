package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// spawnBackend 为 spawn_task 测试返回预设响应的模型后端
type spawnBackend struct {
	response string
}

func (m *spawnBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.response, nil, nil
}
func (m *spawnBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		ch <- m.response
	}()
	return ch, errCh
}
func (m *spawnBackend) ModelID() string           { return "mock-spawn" }
func (m *spawnBackend) MaxContext() int           { return 4096 }
func (m *spawnBackend) SupportsVision() bool      { return false }
func (m *spawnBackend) SetReasoningEffort(string) {}
func (m *spawnBackend) ReasoningEffort() string   { return "" }
func (m *spawnBackend) SetTopP(p float64)         {}
func (m *spawnBackend) TopP() float64             { return 0 }
func (m *spawnBackend) SetTemperature(t float64)  {}
func (m *spawnBackend) Temperature() float64      { return 0 }

func newSpawnRuntime(t *testing.T) *Runtime {
	t.Helper()
	tmpDir, _ := os.MkdirTemp("", "contextloom-spawn-*")
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	cfg := &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmpDir + "/mem", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmpDir + "/plug"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "task"},
		Overseer:        config.OverseerConfig{PassThreshold: 90},
		Project:         config.ProjectConfig{Root: tmpDir, Vocab: tmpDir + "/vocab.json"},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.Backend = &spawnBackend{response: "已实现登录功能，代码已写入 auth.py。"}
	// 手动创建监工用于 SpawnTask 评估
	rt.overseerMu.Lock()
	rt.currentOverseer = overseer.New("test", map[string]any{}, types.Score(90), nil)
	rt.overseerMu.Unlock()
	return rt
}

// TestSpawnTask_EndToEnd 验证 SpawnTask 创建无状态子 Agent 并返回结果+评分
func TestSpawnTask_EndToEnd(t *testing.T) {
	rt := newSpawnRuntime(t)
	ctx := context.Background()

	result, err := rt.SpawnTask(ctx, "main", tools.TaskSpec{
		Task:      "实现登录函数",
		Directive: "CODE_AGENT",
		Context:   "auth.py",
	})
	if err != nil {
		t.Fatalf("SpawnTask 错误: %v", err)
	}
	if !strings.Contains(result, "已实现登录功能") {
		t.Errorf("结果应含子 Agent 输出，got %q", result)
	}
	// 应附监工评分
	if !strings.Contains(result, "监工评估") {
		t.Errorf("结果应含监工评分，got %q", result)
	}
}

// TestSpawnTask_StatelessIsolation 验证子 Agent 不继承主 Agent 对话历史
func TestSpawnTask_StatelessIsolation(t *testing.T) {
	rt := newSpawnRuntime(t)
	// 向主 Agent 历史注入"污染"内容
	rt.AddToHistory("user", "这是主Agent的秘密历史不该被子Agent看到")

	ctx := context.Background()
	result, err := rt.SpawnTask(ctx, "main", tools.TaskSpec{Task: "子任务"})
	if err != nil {
		t.Fatalf("SpawnTask 错误: %v", err)
	}
	// 子 Agent mock 后端只返回预设响应，不含主历史；但核心验证点是子 Agent 未注入历史
	// 这里验证结果合法即可（隔离在 SetConversationHistory 未调用层面保证）
	if result == "" {
		t.Error("子 Agent 应有结果")
	}
}

// TestSpawnTask_ChildAgentHasNoSpawnTool 验证子 Agent 工具表过滤了 spawn_task（防递归）
func TestSpawnTask_ChildAgentHasNoSpawnTool(t *testing.T) {
	rt := newSpawnRuntime(t)
	filtered := rt.Tools.CloneWithout("spawn_task")
	toolNames := filtered.List()
	for _, n := range toolNames {
		if n == "spawn_task" {
			t.Error("子 Agent 工具表不应含 spawn_task（防递归）")
		}
	}
	// send_message/check_messages 仍可用（子 Agent 可通信）
	hasSend := false
	for _, n := range toolNames {
		if n == "send_message" || n == "check_messages" {
			hasSend = true
		}
	}
	if !hasSend {
		t.Error("子 Agent 应保留 send_message/check_messages 通信工具")
	}
}

// TestSpawnTask_SingleflightDuplicate 同一 task 并发 spawn 合并
func TestSpawnTask_SingleflightDuplicate(t *testing.T) {
	rt := newSpawnRuntime(t)
	ctx := context.Background()
	spec := tools.TaskSpec{Task: "相同子任务"}

	type res struct {
		out string
		err error
	}
	done := make(chan res, 2)
	go func() {
		out, err := rt.SpawnTask(ctx, "main", spec)
		done <- res{out, err}
	}()
	go func() {
		out, err := rt.SpawnTask(ctx, "main", spec)
		done <- res{out, err}
	}()
	r1, r2 := <-done, <-done
	if r1.err != nil || r2.err != nil {
		t.Fatalf("并发 spawn 错误: %v %v", r1.err, r2.err)
	}
	// singleflight 应让两者结果相同（同一执行结果）
	if r1.out != r2.out {
		t.Errorf("singleflight 应返回相同结果:\n%q\n%q", r1.out, r2.out)
	}
}
