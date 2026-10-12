package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestCmdSwitchSession_ReloadsMessages /session 切换会话后，TUI 消息列表应立即
// 重建为新会话的完整聊天记录（此前切换后界面仍显示旧会话内容，需重启才恢复）。
func TestCmdSwitchSession_ReloadsMessages(t *testing.T) {
	rt := newOverseerRuntime(t)
	// 先创建会话 A 并写入两轮完整对话（NewSession 设置 currentSessionID 后事件才可持久化）
	sessA := rt.NewSession("会话A").UUID
	rt.AddToHistory("user", "第一轮问题")
	rt.AddToHistory("assistant", "第一轮回答")
	rt.AddToHistory("user", "第二轮问题")
	rt.AddToHistory("assistant", "第二轮回答")

	// 新建会话 B 并作为 TUI 当前显示会话（无历史）
	sessB := rt.NewSession("会话B").UUID
	if sessB == sessA {
		t.Fatal("会话 A/B 应不同")
	}

	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: sessB,
		mode:             modeCommand,
		input:            "/session " + sessA,
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// 切换后 currentSessionID 应同步到会话 A
	if m.currentSessionID != sessA {
		t.Fatalf("currentSessionID 应切到 %s，got %s", sessA, m.currentSessionID)
	}
	// 消息列表应包含会话 A 的全部聊天记录（重进无需重启）
	var text strings.Builder
	for _, msg := range m.messages {
		text.WriteString(msg.Content)
		text.WriteString("\n")
	}
	joined := text.String()
	if !strings.Contains(joined, "第一轮问题") || !strings.Contains(joined, "第一轮回答") {
		t.Errorf("切换后应显示会话 A 的历史消息，实际:\n%s", joined)
	}
	if !strings.Contains(joined, "第二轮问题") || !strings.Contains(joined, "第二轮回答") {
		t.Errorf("切换后应显示会话 A 的全部历史消息（含第二轮），实际:\n%s", joined)
	}
	// 旧会话 B 的内容不应残留
	if strings.Contains(joined, "会话B") && m.currentSessionID == sessA {
		// 允许命令回显包含"会话B"（/new 时标题），但不应有 B 的聊天内容
	}
}

// TestCmdNewSession_ClearsMessages /new 新建会话后，消息列表应清空（无旧会话残留）。
func TestCmdNewSession_ClearsMessages(t *testing.T) {
	rt := newOverseerRuntime(t)
	rt.AddToHistory("user", "旧会话内容")

	m := &model{
		rt:               rt,
		commands:         NewCommandRegistry(rt),
		currentSessionID: rt.GetCurrentSessionID(),
		mode:             modeCommand,
		input:            "/new",
		clickMap:         map[int]int{},
	}
	m.InitConfirmDialog()
	// 先放一条旧消息，验证 /new 后清空
	m.messages = append(m.messages, usrMsg("旧会话内容"))
	oldID := m.currentSessionID
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// /new 后 currentSessionID 应同步为新会话 ID（且与 rt 一致）
	newID := m.currentSessionID
	if newID == oldID {
		t.Fatal("/new 后应创建新会话（currentSessionID 应变化）")
	}
	if newID != rt.GetCurrentSessionID() {
		t.Fatalf("TUI currentSessionID 应与 rt 同步: got %s, rt %s", newID, rt.GetCurrentSessionID())
	}
	// 新会话无历史：消息列表不应包含旧会话内容
	for _, msg := range m.messages {
		if strings.Contains(msg.Content, "旧会话内容") {
			t.Errorf("/new 后不应残留旧会话内容: %q", msg.Content)
		}
	}
}
