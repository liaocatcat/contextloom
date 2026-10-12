package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestChatInputSlashCommand_ExecutesAndLoadsHistory 用户在聊天输入框直接粘贴/快速
// 整行输入 "/load <uuid>" 并回车：应作为命令执行并把历史加载到界面消息列表。
// 历史缺陷：`/` 进入命令模式只在"单字符击键"时触发，整行一次性到达（粘贴/终端
// 快速送达）时命令落入聊天输入框，随后的 Enter 又被"粘贴保护"（recentTyping）
// 当作换行吞掉 → 命令永不执行、历史对话永远无法加载到界面。
func TestChatInputSlashCommand_ExecutesAndLoadsHistory(t *testing.T) {
	rt := newOverseerRuntime(t)
	// 创建会话并写入两轮对话
	rt.NewSession("历史会话")
	rt.AddToHistory("user", "粘贴命令测试问题")
	rt.AddToHistory("assistant", "粘贴命令测试回答")
	sessID := rt.GetCurrentSessionID()

	// 切到另一个新会话，模拟用户当前显示会话
	rt.NewSession("另一个会话")

	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: rt.GetCurrentSessionID(),
		mode:             modeChat,
		chatInput:        "/load " + sessID,
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// 应切到目标会话且历史消息上屏
	if m.currentSessionID != sessID {
		t.Errorf("currentSessionID 应切到 %s，got %s", sessID, m.currentSessionID)
	}
	var joined strings.Builder
	for _, msg := range m.messages {
		joined.WriteString(msg.Content)
		joined.WriteString("\n")
	}
	out := joined.String()
	if !strings.Contains(out, "粘贴命令测试问题") {
		t.Errorf("历史用户消息未加载到界面，实际 messages:\n%s", out)
	}
	if !strings.Contains(out, "粘贴命令测试回答") {
		t.Errorf("历史助手回复未加载到界面，实际 messages:\n%s", out)
	}
}

// TestChatInputMultiline_NotCommand 多行粘贴（含换行）不触发命令执行，
// Enter 仍按原有逻辑插入换行（粘贴保护不回归）。
func TestChatInputMultiline_NotCommand(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("s1")
	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: rt.GetCurrentSessionID(),
		mode:             modeChat,
		chatInput:        "/load abc\n/load def",
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	// 模拟真实粘贴状态（bracketed paste 标记）：Enter 应插入换行而非执行命令
	m.pasting = true
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// 命令不应执行：仍在 chat 模式，输入框保留多行内容（追加一个换行）
	if m.mode != modeChat {
		t.Fatalf("多行粘贴不应退出聊天模式，got mode=%d", m.mode)
	}
	if !strings.Contains(strings.ReplaceAll(m.chatInput, "\n", ""), "/load abc/load def") {
		t.Errorf("多行粘贴的 Enter 应插入换行而非执行命令，got chatInput=%q", m.chatInput)
	}
	// 会话不应被切换
	if m.rt.GetCurrentSessionID() != m.currentSessionID {
		t.Error("多行粘贴不应执行 /load 命令")
	}
}

// TestChatInputSlashQuit_PasteQuit 粘贴 "/quit" 也应作为命令退出（覆盖 quit 分支）。
func TestChatInputSlashQuit_PasteQuit(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("s1")
	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: rt.GetCurrentSessionID(),
		mode:             modeChat,
		chatInput:        "/quit",
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// 粘贴 "/quit" 不应被当作聊天消息发送（不发往模型层）
	if len(m.messages) > 0 {
		for _, msg := range m.messages {
			if msg.Type == MsgUser && strings.Contains(msg.Content, "/quit") {
				t.Errorf("/quit 不应作为用户消息发送: %q", msg.Content)
			}
		}
	}
}

// TestChatInputSlashUnknown_NotSentToModel 聊天输入框整行斜杠文本走命令执行：
// 未知命令应回显"未知命令"提示，绝不当作聊天消息发给模型。
func TestChatInputSlashUnknown_NotSentToModel(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("s1")
	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: rt.GetCurrentSessionID(),
		mode:             modeChat,
		chatInput:        "/nonexistent_cmd xxx",
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	// 非粘贴状态直接回车 → send() 应拦截为命令执行
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// 不应作为用户消息发送给模型
	for _, msg := range m.messages {
		if msg.Type == MsgUser && strings.Contains(msg.Content, "nonexistent_cmd") {
			t.Errorf("未知斜杠命令不应作为用户消息发送: %q", msg.Content)
		}
	}
	// 应出现命令回显（> /nonexistent_cmd ... + 未知命令提示）
	var joined strings.Builder
	for _, msg := range m.messages {
		joined.WriteString(msg.Content)
		joined.WriteString("\n")
	}
	out := joined.String()
	if !strings.Contains(out, "未知命令") {
		t.Errorf("未知命令应回显提示，实际 messages:\n%s", out)
	}
}

// TestChatInputSlashCommand_CommandsExecuted 多条已注册命令整行输入都被执行
// （/sessions 列表 /model 切换 /new 新建等，验证 send 拦截不挑命令）。
func TestChatInputSlashCommand_CommandsExecuted(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.NewSession("s1")

	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: rt.GetCurrentSessionID(),
		mode:             modeChat,
		chatInput:        "/sessions",
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
	if !strings.Contains(out, "对话历史") {
		t.Errorf("/sessions 应通过聊天输入执行并显示会话列表，实际:\n%s", out)
	}
}
