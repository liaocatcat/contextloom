package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestInitialModel_LoadsHistoryOnLoadSession 模拟 --memory-load/--resume 启动路径：
// main.go 先 rt.LoadSession(uuid) 设置 currentSessionID，再 app.Run() → initialModel。
// initialModel 应把该会话的历史消息加载到 m.messages（界面消息列表）。
func TestInitialModel_LoadsHistoryOnLoadSession(t *testing.T) {
	rt := newOverseerRuntime(t)
	// 创建会话并写入两轮对话
	rt.NewSession("历史会话")
	rt.AddToHistory("user", "历史问题一")
	rt.AddToHistory("assistant", "历史回答一")
	rt.AddToHistory("user", "历史问题二")
	rt.AddToHistory("assistant", "历史回答二")

	// 模拟 --memory-load：LoadSession 恢复 currentSessionID + conversationHistory
	if _, _, err := rt.LoadSession(rt.GetCurrentSessionID()); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	m := initialModel(rt)

	// 应复用已加载会话 ID，而非新建
	if m.currentSessionID == "" {
		t.Fatal("currentSessionID 应为已加载会话 ID")
	}
	// 消息列表应包含全部历史对话
	var joined strings.Builder
	for _, msg := range m.messages {
		joined.WriteString(msg.Content)
		joined.WriteString("\n")
	}
	out := joined.String()
	if !strings.Contains(out, "历史问题一") {
		t.Errorf("启动加载后应显示历史用户消息，实际 messages:\n%s", out)
	}
	if !strings.Contains(out, "历史回答一") {
		t.Errorf("启动加载后应显示历史助手回复，实际 messages:\n%s", out)
	}
	if !strings.Contains(out, "历史问题二") || !strings.Contains(out, "历史回答二") {
		t.Errorf("启动加载后应显示全部两轮历史，实际 messages:\n%s", out)
	}
}

// TestInitialModel_NoSessionCreatesNew 无已加载会话时 initialModel 应新建会话（空消息列表）。
func TestInitialModel_NoSessionCreatesNew(t *testing.T) {
	rt := newOverseerRuntime(t)
	m := initialModel(rt)
	if m.currentSessionID == "" {
		t.Fatal("无加载会话时应新建会话")
	}
	if len(m.messages) != 0 {
		t.Errorf("新会话消息列表应为空，got %d 条", len(m.messages))
	}
}

// TestCommandLoad_ReloadsHistory /load 命令执行后消息列表应重建为指定会话历史。
func TestCommandLoad_ReloadsHistory(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("历史会话")
	rt.AddToHistory("user", "/load 测试问题")
	rt.AddToHistory("assistant", "/load 测试回答")
	sessID := rt.GetCurrentSessionID()

	// 切换到一个新会话作为当前显示
	rt.NewSession("另一个会话")

	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: rt.GetCurrentSessionID(),
		mode:             modeCommand,
		input:            "/load " + sessID,
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var joined strings.Builder
	for _, msg := range m.messages {
		joined.WriteString(msg.Content)
		joined.WriteString("\n")
	}
	out := joined.String()
	if !strings.Contains(out, "/load 测试问题") || !strings.Contains(out, "/load 测试回答") {
		t.Errorf("/load 后应显示目标会话历史，实际:\n%s", out)
	}
}

// TestCommandSession_LoadsToolEvents /session 切换后工具调用事件也应显示。
// 注意：tool_call/tool_result 事件目前由各写入方产生（Data 可能为 string 或 map），
// TUI 需兼容两种形态，缺失则历史记录不完整。
func TestCommandSession_LoadsToolEvents(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("工具会话")
	rt.AddToHistory("user", "请使用工具")
	rt.AddToHistory("assistant", "好的，正在使用工具")

	// 手动写入工具事件（模拟真实运行中工具调用被持久化）
	sessID := rt.GetCurrentSessionID()
	rt.Memory.AppendEvent(sessID, typesSessionEvent("tool_call", map[string]any{
		"name":      "read_file",
		"arguments": `{"path":"a.go"}`,
	}))
	rt.Memory.AppendEvent(sessID, typesSessionEvent("tool_result", map[string]any{
		"name":   "read_file",
		"result": "文件内容",
	}))

	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: "another-session-001",
		mode:             modeCommand,
		input:            "/session " + sessID,
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	var joined strings.Builder
	for _, msg := range m.messages {
		joined.WriteString(msg.Content)
		joined.WriteString(" " + msg.ToolName + "\n")
	}
	out := joined.String()
	if !strings.Contains(out, "read_file") {
		t.Errorf("/session 后应显示工具调用记录，实际:\n%s", out)
	}
}

// typesSessionEvent 构造一个 SessionEvent
func typesSessionEvent(typ string, data any) *types.SessionEvent {
	return &types.SessionEvent{
		Seq:       time.Now().UnixNano(),
		Timestamp: time.Now(),
		Type:      typ,
		Data:      data,
	}
}
