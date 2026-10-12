package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// msgBackend 流式返回预设响应（带工具可选）
type msgBackend struct {
	resp string
}

func (m *msgBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.resp, nil, nil
}
func (m *msgBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		ch <- m.resp
	}()
	return ch, errCh
}
func (m *msgBackend) ModelID() string           { return "mock-msg" }
func (m *msgBackend) MaxContext() int           { return 4096 }
func (m *msgBackend) SupportsVision() bool      { return false }
func (m *msgBackend) SetReasoningEffort(string) {}
func (m *msgBackend) ReasoningEffort() string   { return "" }
func (m *msgBackend) SetTopP(p float64)         {}
func (m *msgBackend) TopP() float64             { return 0 }
func (m *msgBackend) SetTemperature(t float64)  {}
func (m *msgBackend) Temperature() float64      { return 0 }

// TestMessageBus_DrainInbox 验证 DrainInbox 非阻塞读取并清空
func TestMessageBus_DrainInbox(t *testing.T) {
	mb := NewMessageBus()
	ch := mb.Register("sub-1")

	mb.Send(types.AgentMessage{From: "main", To: "sub-1", Type: "reply", Content: "hello"})
	mb.Send(types.AgentMessage{From: "main", To: "sub-1", Type: "reply", Content: "world"})
	// 给 channel 一点时间入队
	time.Sleep(10 * time.Millisecond)

	msgs := mb.DrainInbox("sub-1")
	if len(msgs) != 2 {
		t.Fatalf("应读取 2 条，got %d", len(msgs))
	}
	if mb.DrainInbox("sub-1") != nil {
		if n := len(mb.DrainInbox("sub-1")); n != 0 {
			t.Errorf("drain 后应清空，got %d", n)
		}
	}
	_ = ch
}

// TestAgent_DrainInboxIntoMessages 验证 Agent.RunStream 每轮把收件箱消息注入对话
func TestAgent_DrainInboxIntoMessages(t *testing.T) {
	mb := NewMessageBus()
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	toolReg := tools.NewRegistry()

	// 用一个捕获 messages 的后端不便验证 drain；改为直接验证 drain 行为：
	// 构造带 bus 的 Agent，主 goroutine 发消息，RunStream 应消费
	exec := New(eng, toolReg)
	exec.SetBus(mb)

	agent := &Agent{
		ID:      "drain-test",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "测试 drain"},
		harness: eng,
		tools:   toolReg,
		backend: &msgBackend{resp: "已收到消息并处理"},
	}
	agent.bus = mb
	mb.Register("drain-test")

	// 预先投递消息到 inbox
	mb.Send(types.AgentMessage{From: "main", To: "drain-test", Type: "notify", Content: "补充上下文：文件在 /tmp"})

	ctx := context.Background()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}

	var got strings.Builder
	for token := range ch {
		got.WriteString(token)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream error: %v", e)
	}

	if got.String() == "" {
		t.Error("Agent 应有输出")
	}
	// 消息应被 drain（inbox 清空）
	if remaining := mb.DrainInbox("drain-test"); len(remaining) > 0 {
		t.Errorf("RunStream 后 inbox 应清空，剩 %d 条", len(remaining))
	}
}

// TestSpawnTool_CloneWithout 验证过滤 spawn_task 防递归
func TestSpawnTool_CloneWithout(t *testing.T) {
	r := tools.NewRegistry()
	// 模拟 runtime 注册 spawn_task
	r.Register("spawn_task", func(ctx context.Context, p map[string]any) (any, error) {
		return "child", nil
	})
	origHas := false
	for _, n := range r.List() {
		if n == "spawn_task" {
			origHas = true
		}
	}
	if !origHas {
		t.Fatal("原注册表应含 spawn_task")
	}
	// CloneWithout 过滤后子 Agent 不可见 spawn_task
	filtered := r.CloneWithout("spawn_task")
	for _, n := range filtered.List() {
		if n == "spawn_task" {
			t.Error("CloneWithout 应过滤 spawn_task")
		}
	}
}
