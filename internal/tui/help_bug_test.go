package tui

import (
	"strings"
	"testing"

	bubbletea "github.com/charmbracelet/bubbletea"
)

// Bug: help 打开时再按 ? 应关闭（toggle），而不是保持打开
func TestHelp_ToggleByQuestionMark(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.width, m.height = 100, 30

	// 按 ? 打开
	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("?")})
	mm := res.(*model)
	if !mm.showHelp {
		t.Fatal("? 应打开帮助面板")
	}

	// 再按 ? 应关闭（帮助面板提示 "? 关闭本面板"）
	res, _ = mm.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("?")})
	mm = res.(*model)
	if mm.showHelp {
		t.Error("BUG: help 打开时再按 ? 无法关闭（面板保持打开）")
	}
}

// Bug: help 关闭后渲染不应残留帮助内容
func TestHelp_CloseNoResidueInRender(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.width, m.height = 100, 30

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("?")})
	mm := res.(*model)
	if !mm.showHelp {
		t.Fatal("? 应打开帮助面板")
	}
	if out := mm.View(); !strings.Contains(out, "快捷键") {
		t.Fatal("help 打开时 View 应包含帮助面板内容")
	}

	// 按 ESC 关闭
	res, _ = mm.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyEsc})
	mm = res.(*model)
	if mm.showHelp {
		t.Fatal("ESC 应关闭帮助面板")
	}
	out := mm.View()
	if strings.Contains(out, "快捷键") {
		t.Error("BUG: help 关闭后 View 仍包含帮助内容（残留）")
	}
}

// ── Windows 文本复制回归：鼠标捕获不应无条件启用 ──

// TestKeyT_TogglesLastThinkingBlock 验证 t 键（Windows 无鼠标时的键盘兜底）
// 能展开/折叠最近一条已完成的思考消息。
func TestKeyT_TogglesLastThinkingBlock(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeNormal
	m.messages = []ChatMessage{
		asstMsg("第一条普通回复"),
		{Type: MsgThinking, Content: "<thinking>思考一</thinking>", Collapsed: true, Streaming: false},
		asstMsg("第二条普通回复"),
		{Type: MsgThinking, Content: "<thinking>思考二</thinking>", Collapsed: false, Streaming: false},
	}
	keyT := bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("t")}
	// 按 t：应切换最近的思考消息（思考二：false → true）
	nm, _ := m.handleKey(keyT)
	m2 := nm.(*model)
	if !m2.messages[3].Collapsed {
		t.Fatalf("t 键未切换最近思考块：期望折叠=true，实际=%v", m2.messages[3].Collapsed)
	}
	// 再按 t：切换回展开
	nm2, _ := m2.handleKey(keyT)
	m3 := nm2.(*model)
	if m3.messages[3].Collapsed {
		t.Fatalf("t 键二次未切换：期望折叠=false，实际=%v", m3.messages[3].Collapsed)
	}
	// 更早的思考块不应被影响
	if !m3.messages[1].Collapsed {
		t.Fatalf("t 键误操作了非最近思考块：期望保持折叠=true")
	}
}

// TestKeyT_DoesNotFireInChatMode 验证 chat 模式输入框内按 t 是普通字符，不触发折叠。
func TestKeyT_DoesNotFireInChatMode(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.messages = []ChatMessage{
		{Type: MsgThinking, Content: "<thinking>x</thinking>", Collapsed: true, Streaming: false},
	}
	nm, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("t")})
	m2 := nm.(*model)
	if m2.messages[0].Collapsed != true {
		t.Fatalf("chat 模式下 t 不应触发思考块折叠，实际 Collapsed=%v", m2.messages[0].Collapsed)
	}
	if m2.chatInput != "t" {
		t.Fatalf("chat 模式下 t 应作为输入字符，实际 chatInput=%q", m2.chatInput)
	}
}

// TestRunProgramOptions_MouseEnabledAllPlatforms 说明性测试：全平台启用鼠标
// 捕获（Windows 也可点击展开/折叠思考块、滚轮滚动）。
// Windows 启用鼠标捕获后原生拖选复制被鼠标事件接管：
//   - Windows Terminal：按住 Shift 拖选复制（推荐）
//   - cmd/conhost：右键 → 标记 后拖选复制
//
// 键盘替代仍保留：滚动 PgUp/PgDown、思考块展开/折叠 t 键。
func TestRunProgramOptions_MouseEnabledAllPlatforms(t *testing.T) {
	t.Log("全平台: 鼠标捕获启用（滚轮滚动 + 点击展开思考块）；Windows 复制用 Shift+拖选 或 cmd 右键标记")
}
