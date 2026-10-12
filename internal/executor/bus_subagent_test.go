package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// toolCallBackend 后端在首轮返回一个工具调用（send_message / check_messages），
// 后续轮直接返回文本结束，用于端到端验证子 Agent 工具执行时的 AgentID 注入。
type toolCallBackend struct {
	resp      string
	toolCall  *types.ToolCall
	callCount int
	seenMsgs  [][]types.Message // 记录每轮请求消息，验证 drain 注入
}

func (m *toolCallBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	m.callCount++
	m.seenMsgs = append(m.seenMsgs, messages)
	if m.callCount == 1 && m.toolCall != nil {
		return m.resp, []types.ToolCall{*m.toolCall}, nil
	}
	return "done", nil, nil
}
func (m *toolCallBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		ch <- m.resp
	}()
	return ch, errCh
}
func (m *toolCallBackend) ModelID() string           { return "mock-toolcall" }
func (m *toolCallBackend) MaxContext() int           { return 8192 }
func (m *toolCallBackend) SupportsVision() bool      { return false }
func (m *toolCallBackend) SetReasoningEffort(string) {}
func (m *toolCallBackend) ReasoningEffort() string   { return "" }
func (m *toolCallBackend) SetTopP(p float64)         {}
func (m *toolCallBackend) TopP() float64             { return 0 }
func (m *toolCallBackend) SetTemperature(t float64)  {}
func (m *toolCallBackend) Temperature() float64      { return 0 }

// TestSubAgent_SendMessage_AgentID 验证子 Agent（RunStreamWithBudget 路径）执行
// send_message 时 From 是否正确为子 Agent 自己的 ID。
// 根因：RunStreamWithBudget 未注入 WithAgentID → AgentIDFromContext 返回 "main"，
// 导致子 Agent 发出的消息 From=main（寻址错乱）。
func TestSubAgent_SendMessage_AgentID(t *testing.T) {
	mb := NewMessageBus()
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	reg := tools.NewRegistry()
	reg.Register("send_message", tools.MakeSendMessageTool(mb))
	reg.Register("check_messages", tools.MakeCheckMessagesTool(mb))

	// 主 Agent 收件箱注册（send_message 目标是 main）
	mb.Register("main")

	be := &toolCallBackend{
		resp: "发送消息",
		toolCall: &types.ToolCall{
			ID:   "call-1",
			Type: "function",
			Function: types.FunctionCall{
				Name:      "send_message",
				Arguments: `{"to":"main","content":"子任务汇报：完成"}`,
			},
		},
	}
	agent := &Agent{
		ID:      "sub-1",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "子任务", Manifest: &types.ContextManifest{MaxIterations: 15, MaxTokens: 8000}},
		harness: eng,
		tools:   reg,
		backend: be,
		bus:     mb,
	}
	mb.Register("sub-1")

	ch, errCh, err := agent.RunStreamWithBudget(context.Background(), 15)
	if err != nil {
		t.Fatalf("RunStreamWithBudget: %v", err)
	}
	var out strings.Builder
	for tok := range ch {
		out.WriteString(tok)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream err: %v", e)
	}
	_ = out

	// 检查 main 的收件箱：若 From 正确为 sub-1，则消息应投递到 main 的 inbox
	msgs := mb.DrainInbox("main")
	if len(msgs) != 1 {
		t.Fatalf("send_message(to=main) 应投递 1 条到 main，got %d（子 Agent From 可能被错误识别为 main 而自投）", len(msgs))
	}
	if msgs[0].From != "sub-1" {
		t.Errorf("send_message From 应为 sub-1，got %q", msgs[0].From)
	}
}

// TestSubAgent_CheckMessages_DrainsOwnInbox 验证子 Agent check_messages 应读取
// 自己（sub-1）的收件箱，而非 main 的。
// 根因：AgentIDFromContext 未注入 → 回退 "main" → check_messages 读的是 main 的
// 收件箱，子 Agent 永远读不到别人发给自己的消息。
func TestSubAgent_CheckMessages_DrainsOwnInbox(t *testing.T) {
	mb := NewMessageBus()
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	reg := tools.NewRegistry()
	reg.Register("send_message", tools.MakeSendMessageTool(mb))
	reg.Register("check_messages", tools.MakeCheckMessagesTool(mb))
	mb.Register("main")
	mb.Register("sub-1")

	// 别人发给 sub-1 的消息（应被子 Agent 读到）
	mb.Send(types.AgentMessage{From: "main", To: "sub-1", Type: "notify", Content: "给你补充信息"})

	be := &toolCallBackend{
		resp: "查消息",
		toolCall: &types.ToolCall{
			ID:   "call-1",
			Type: "function",
			Function: types.FunctionCall{
				Name:      "check_messages",
				Arguments: `{}`,
			},
		},
	}
	agent := &Agent{
		ID:      "sub-1",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "子任务", Manifest: &types.ContextManifest{MaxIterations: 15, MaxTokens: 8000}},
		harness: eng,
		tools:   reg,
		backend: be,
		bus:     mb,
	}

	ch, errCh, err := agent.RunStreamWithBudget(context.Background(), 15)
	if err != nil {
		t.Fatalf("RunStreamWithBudget: %v", err)
	}
	var out strings.Builder
	for tok := range ch {
		out.WriteString(tok)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream err: %v", e)
	}
	_ = out

	// 若 AgentID 注入正确，sub-1 的 inbox 应已被 drain（消息被消费）；
	// 若回退 main，则 sub-1 的 inbox 原封不动。
	remaining := mb.DrainInbox("sub-1")
	if len(remaining) != 0 {
		t.Errorf("check_messages 应 drain 子 Agent 自己的 inbox，剩 %d 条（AgentID 未注入，读了 main 的收件箱）", len(remaining))
	}
}

// TestSubAgent_RunStreamWithBudget_DrainsInbox 验证子 Agent 每轮应把收件箱消息
// 注入对话（与 RunStream 主 Agent 路径一致）。
// 当前 RunStreamWithBudget 无 drain 逻辑 → 发给子 Agent 的消息永远不进模型上下文。
func TestSubAgent_RunStreamWithBudget_DrainsInbox(t *testing.T) {
	mb := NewMessageBus()
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	reg := tools.NewRegistry()

	be := &toolCallBackend{resp: "收到"}
	agent := &Agent{
		ID:      "sub-1",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "子任务", Manifest: &types.ContextManifest{MaxIterations: 15, MaxTokens: 8000}},
		harness: eng,
		tools:   reg,
		backend: be,
		bus:     mb,
	}
	mb.Register("sub-1")
	// 预先投递消息
	mb.Send(types.AgentMessage{From: "main", To: "sub-1", Type: "notify", Content: "中途补充：注意目录"})

	ch, errCh, err := agent.RunStreamWithBudget(context.Background(), 15)
	if err != nil {
		t.Fatalf("RunStreamWithBudget: %v", err)
	}
	for range ch {
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream err: %v", e)
	}

	// 若子 Agent 有 drain 逻辑，inbox 应被消费；当前实现无 → 仍剩 1 条
	remaining := mb.DrainInbox("sub-1")
	if len(remaining) != 0 {
		t.Errorf("子 Agent RunStreamWithBudget 应 drain 收件箱，剩 %d 条（消息未注入子 Agent 上下文）", len(remaining))
	}
}
