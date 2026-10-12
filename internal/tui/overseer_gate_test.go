package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// gateMockBackend 是监工门控测试用的最小模型后端实现
type gateMockBackend struct{ response string }

func (m *gateMockBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.response, nil, nil
}
func (m *gateMockBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		ch <- m.response
	}()
	return ch, errCh
}
func (m *gateMockBackend) ModelID() string           { return "mock" }
func (m *gateMockBackend) MaxContext() int           { return 4096 }
func (m *gateMockBackend) SupportsVision() bool      { return false }
func (m *gateMockBackend) SetReasoningEffort(string) {}
func (m *gateMockBackend) ReasoningEffort() string   { return "" }
func (m *gateMockBackend) SetTopP(p float64)         {}
func (m *gateMockBackend) TopP() float64             { return 0 }
func (m *gateMockBackend) SetTemperature(t float64)  {}
func (m *gateMockBackend) Temperature() float64      { return 0 }

// newOverseerRuntime 构造 task 模式带监工的真实 runtime
func newOverseerRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-tui-gate-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmpDir + "/memory", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmpDir + "/plugins"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "task"},
		Overseer:        config.OverseerConfig{PassThreshold: 90},
		Project:         config.ProjectConfig{Root: tmpDir, Vocab: tmpDir + "/vocab.json"},
	}
	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	rt.Backend = &gateMockBackend{response: "ok"}

	// task 模式 ProcessInput 一次以创建监工
	ctx := context.Background()
	ch, _, err := rt.ProcessInput(ctx, "单任务")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	for range ch {
	}
	if !rt.HasOverseer() {
		t.Fatal("task 模式应创建监工")
	}
	return rt
}

// historyContains 检查对话历史是否含指定内容
func historyContains(rt *runtime.Runtime, want string) bool {
	for _, msg := range rt.GetConversationHistory() {
		if s, ok := msg.Content.(string); ok && strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// lastSysMsg 返回最后一条系统消息内容
func lastSysMsg(m *model) string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Type == MsgSystem && strings.Contains(m.messages[i].Content, "监工评估") {
			return m.messages[i].Content
		}
	}
	return ""
}

// TestStreamDone_OverseerGate_Passed 高质量响应通过 → 纳入对话历史
func TestStreamDone_OverseerGate_Passed(t *testing.T) {
	rt := newOverseerRuntime(t)
	good := "已实现登录模块，包含密码校验与令牌生成，逻辑完整。已编写单元测试，go test 全部通过，覆盖率达标。"

	m := &model{rt: rt, streamBuf: good, streaming: true, clickMap: map[int]int{}}
	m.Update(streamDoneMsg{})

	if !historyContains(rt, good) {
		t.Error("通过监工评估的响应应纳入对话历史")
	}
	if got := lastSysMsg(m); !strings.Contains(got, "✅") {
		t.Errorf("通过应显示 ✅，got %q", got)
	}
}

// TestStreamDone_OverseerGate_Rejected 低质量响应未通过 → 仍纳入历史（聊天记录完整），界面显示⚠提示
func TestStreamDone_OverseerGate_Rejected(t *testing.T) {
	rt := newOverseerRuntime(t)
	bad := "好" // 空泛响应：completeness 60，总分远低于阈值

	m := &model{rt: rt, streamBuf: bad, streaming: true, clickMap: map[int]int{}}
	m.Update(streamDoneMsg{})

	// 用户已看到该回复：必须入史，否则重进会话聊天记录断档（user 无配对 assistant）
	if !historyContains(rt, bad) {
		t.Error("未通过监工评估的响应也应纳入对话历史（聊天记录完整性优先）")
	}
	if got := lastSysMsg(m); !strings.Contains(got, "⚠") {
		t.Errorf("未通过应显示 ⚠ 提示，got %q", got)
	}
}

// TestStreamErr_PartialReplySaved 流错误时已产生的部分回复应写入历史（防整轮丢失）
func TestStreamErr_PartialReplySaved(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("测试会话") // 设置 currentSessionID，确保事件可持久化
	partial := "正在修改登录模块："

	m := &model{rt: rt, streamBuf: partial, streaming: true, clickMap: map[int]int{}}
	m.Update(streamErrMsg{err: "连接中断"})

	if !historyContains(rt, partial) {
		t.Error("流错误前已产生的部分回复应写入对话历史")
	}
	// 历史中应能找回该消息（模拟重进会话）
	events, err := rt.Memory.LoadEvents(rt.GetCurrentSessionID())
	if err != nil || len(events) == 0 {
		t.Fatalf("应持久化事件: %v", err)
	}
	found := false
	for _, evt := range events {
		if evt.Type == "assistant_msg" {
			if s, ok := evt.Data.(string); ok && strings.Contains(s, partial) {
				found = true
			}
		}
	}
	if !found {
		t.Error("部分回复应持久化到 JSONL 事件日志")
	}
}

// TestInterrupt_PartialReplySaved Ctrl+C 中断时已产生的部分回复应写入历史
func TestInterrupt_PartialReplySaved(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("测试会话")
	partial := "已完成第一步："

	m := &model{rt: rt, streamBuf: partial, streaming: true, clickMap: map[int]int{}}
	// 第一次 Ctrl+C（interrupted=false）：优雅中断并保存已产生的回复
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if !historyContains(rt, partial) {
		t.Error("中断前已产生的回复应写入对话历史")
	}
	// 中断后应保持运行（第二次 Ctrl+C 才退出）
	if m.interrupted != true {
		t.Error("第一次 Ctrl+C 后应标记 interrupted")
	}
}

// newStatusBarRuntime 构造带预算的最小 runtime（供状态栏 ctx 显示测试）
func newStatusBarRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-tui-statusbar-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmpDir + "/memory", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmpDir + "/plugins"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium"},
		Project:         config.ProjectConfig{Root: tmpDir, Vocab: tmpDir + "/vocab.json"},
	}
	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return rt
}
