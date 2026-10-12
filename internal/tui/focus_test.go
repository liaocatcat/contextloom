package tui

import (
	"strings"
	"testing"

	bubbletea "github.com/charmbracelet/bubbletea"
	tools "github.com/contextloom/contextloom-agent/internal/tools"
)

// ── 焦点区分测试（Windows 点击 TUI 历史记录后，滚轮/上下键不得再作用于输入框）──
// 背景：鼠标捕获启用后用户可点击消息历史区"聚焦"到历史。旧行为 ↑↓ 在 chat 模式
// 始终回填【输入历史】到输入框——即使焦点已点在历史记录里，上下键仍"作用于输入框"。
// 修复：点击历史区 → focusHistory=true，↑↓/PgUp/PgDown/滚轮全部滚动消息历史；
// 点击输入区/输入任意键 → 焦点交还输入框（↑↓ 恢复回填输入历史）。

// focusTestModel 构造长消息列表模型并渲染一帧（viewBodyH/viewInputTop 就位）。
func focusTestModel(t *testing.T) *model {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.width, m.height = 100, 30
	m.history = nil // 忽略磁盘历史，保证确定性
	for i := 0; i < 40; i++ {
		m.appendMsg(asstMsg("历史消息 " + itoa(i)))
	}
	m.renderCache = make(map[int]*msgRenderCache)
	m.View() // 渲染一帧：viewBodyH/viewInputTop/clickMap 就位
	if m.viewInputTop <= 0 {
		t.Fatalf("viewInputTop 未设置: %d", m.viewInputTop)
	}
	if m.viewBodyH <= 0 {
		t.Fatalf("viewBodyH 未设置: %d", m.viewBodyH)
	}
	return m
}

// 点击历史区后：↑↓ 滚动消息历史，输入框内容/光标/输入历史索引全部不动。
func TestFocusHistory_UpDownScrollsHistory_NotInput(t *testing.T) {
	m := focusTestModel(t)
	m.scrollOffset = 0
	m.chatInput = "已有输入"
	m.cursor = len([]rune(m.chatInput))

	// 点击历史区（Y=0 < viewInputTop）→ 焦点进入历史
	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: 0})
	m2 := res.(*model)
	if !m2.focusHistory {
		t.Fatalf("点击历史区后 focusHistory 应为 true")
	}
	before := m2.scrollOffset
	// ↑ → 滚动历史，输入框分毫不动
	res2, _ := m2.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyUp})
	m3 := res2.(*model)
	if m3.scrollOffset <= before {
		t.Fatalf("焦点在历史时 ↑ 应向上滚动历史, before=%d after=%d", before, m3.scrollOffset)
	}
	if m3.chatInput != "已有输入" || m3.cursor != len([]rune("已有输入")) {
		t.Fatalf("焦点在历史时 ↑ 不得修改输入框: input=%q cursor=%d", m3.chatInput, m3.cursor)
	}
	if m3.historyIdx != 0 {
		t.Fatalf("焦点在历史时 ↑ 不得回填输入历史, historyIdx=%d", m3.historyIdx)
	}
	if !m3.focusHistory {
		t.Fatalf("↑ 滚动后焦点应保持在历史区")
	}
	// ↓ → 向下滚动（回底部）
	before = m3.scrollOffset
	res3, _ := m3.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyDown})
	m4 := res3.(*model)
	if m4.scrollOffset >= before {
		t.Fatalf("焦点在历史时 ↓ 应向下滚动, before=%d after=%d", before, m4.scrollOffset)
	}
	if m4.chatInput != "已有输入" {
		t.Fatalf("焦点在历史时 ↓ 不得修改输入框")
	}
}

// 点击输入区 → 焦点交还输入框：↑↓ 恢复回填输入历史（原行为）。
func TestFocusHistory_ClickInputArea_ReturnsFocus(t *testing.T) {
	m := focusTestModel(t)
	m.history = []string{"历史输入一", "历史输入二"}
	m.focusHistory = true
	m.chatInput = ""
	m.cursor = 0

	// 点击输入区（Y = viewInputTop+1，输入框内）→ 焦点回输入框
	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: m.viewInputTop + 1})
	m2 := res.(*model)
	if m2.focusHistory {
		t.Fatalf("点击输入区后 focusHistory 应为 false")
	}
	// ↑ → 回填输入历史（原行为）
	res2, _ := m2.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyUp})
	m3 := res2.(*model)
	if m3.chatInput != "历史输入二" {
		t.Fatalf("焦点在输入框时 ↑ 应回填输入历史, got %q", m3.chatInput)
	}
}

// 焦点在历史时输入任意字符 → 焦点交还输入框，字符进入输入框。
func TestFocusHistory_TypingReturnsFocusToInput(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = true
	m.chatInput = ""
	m.cursor = 0

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("你")})
	m2 := res.(*model)
	if m2.focusHistory {
		t.Fatalf("输入字符后 focusHistory 应为 false")
	}
	if m2.chatInput != "你" {
		t.Fatalf("输入字符应进入输入框, got %q", m2.chatInput)
	}
}

// 焦点在历史时滚轮一律滚动消息历史——即使光标悬停在输入框上（滚轮不得滚动输入区）。
func TestFocusHistory_WheelAtInputArea_StillScrollsHistory(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = true
	before := m.scrollOffset

	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonWheelUp, X: 5, Y: m.viewInputTop + 1})
	m2 := res.(*model)
	if m2.scrollOffset <= before {
		t.Fatalf("焦点在历史时滚轮应滚动历史(即使光标在输入区), before=%d after=%d", before, m2.scrollOffset)
	}
	if m2.inputScroll != 0 {
		t.Fatalf("焦点在历史时滚轮不得滚动输入区, inputScroll=%d", m2.inputScroll)
	}
	if !m2.focusHistory {
		t.Fatalf("滚轮滚动不应改变焦点状态")
	}
}

// 焦点在输入框 + 光标在输入区 + 多行输入 → 滚轮滚动输入区（不碰消息历史）。
func TestFocusHistory_WheelOverInput_ScrollsInput(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = false
	// 构造超过显示区的多行输入
	m.chatInput = ""
	for i := 0; i < 30; i++ {
		m.chatInput += "line" + itoa(i) + "\n"
	}
	m.cursor = len([]rune(m.chatInput))
	m.inputScroll = 0
	beforeScroll := m.scrollOffset

	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonWheelUp, X: 5, Y: m.viewInputTop + 1})
	m2 := res.(*model)
	if m2.inputScroll <= 0 {
		t.Fatalf("焦点在输入框且光标在输入区时滚轮应滚动输入区, inputScroll=%d", m2.inputScroll)
	}
	if m2.scrollOffset != beforeScroll {
		t.Fatalf("滚动输入区时消息历史不得滚动, scrollOffset=%d", m2.scrollOffset)
	}
	// 多行输入可滚动时滚轮向下回滚输入区
	before2 := m2.inputScroll
	res2, _ := m2.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonWheelDown, X: 5, Y: m.viewInputTop + 1})
	m3 := res2.(*model)
	if m3.inputScroll >= before2 {
		t.Fatalf("滚轮向下应回滚输入区, before=%d after=%d", before2, m3.inputScroll)
	}
}

// 焦点在输入框 + 光标在消息区 → 滚轮滚动消息历史（原行为保留）。
func TestFocusHistory_WheelOverBody_ScrollsHistory(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = false
	before := m.scrollOffset

	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonWheelUp, X: 5, Y: 0})
	m2 := res.(*model)
	if m2.scrollOffset <= before {
		t.Fatalf("光标在消息区时滚轮应滚动历史, before=%d after=%d", before, m2.scrollOffset)
	}
}

// 焦点在历史时 PgUp/PgDown 也滚动历史且保持焦点。
func TestFocusHistory_PgUpDownScrollsHistory(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = true
	before := m.scrollOffset

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyPgUp})
	m2 := res.(*model)
	if m2.scrollOffset <= before {
		t.Fatalf("焦点在历史时 PgUp 应滚动历史, before=%d after=%d", before, m2.scrollOffset)
	}
	if !m2.focusHistory {
		t.Fatalf("PgUp 滚动后焦点应保持在历史区")
	}
	before2 := m2.scrollOffset
	res2, _ := m2.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyPgDown})
	m3 := res2.(*model)
	if m3.scrollOffset >= before2 {
		t.Fatalf("焦点在历史时 PgDown 应向下滚动, before=%d after=%d", before2, m3.scrollOffset)
	}
}

// send 后焦点回到输入框。
func TestFocusHistory_ClearedOnSend(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = true
	m.chatInput = "发送测试"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.send()
	m2 := res.(*model)
	if m2.focusHistory {
		t.Fatalf("send 后 focusHistory 应为 false")
	}
}

// View 计算的 viewInputTop 应指向输入区分隔线（输入区第一行）。
func TestView_ComputesInputTop(t *testing.T) {
	m := focusTestModel(t)
	out := m.View()
	lines := strings.Split(out, "\n")
	if m.viewInputTop < 0 || m.viewInputTop >= len(lines) {
		t.Fatalf("viewInputTop 越界: %d (lines=%d)", m.viewInputTop, len(lines))
	}
	// 输入区顶行应为分隔线（renderInputArea 首行输出 ─ 分隔）
	if !strings.Contains(lines[m.viewInputTop], "─") {
		t.Fatalf("viewInputTop=%d 行应为输入区分隔线, got %q", m.viewInputTop, lines[m.viewInputTop])
	}
	// 输入区之下应能找到状态栏（含"模型:"字样）
	found := false
	for i := m.viewInputTop; i < len(lines); i++ {
		if strings.Contains(lines[i], "模型") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("输入区之下应能找到状态栏")
	}
}

// 状态栏在焦点于历史区时显示焦点提示。
func TestRenderStatusBar_FocusHint(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = true
	bar := renderStatusBar(m)
	if !strings.Contains(bar, "焦点") {
		t.Fatalf("焦点在历史时状态栏应显示焦点提示, got: %s", bar)
	}
	m.focusHistory = false
	bar2 := renderStatusBar(m)
	if strings.Contains(bar2, "焦点") {
		t.Fatalf("焦点在输入框时状态栏不应显示历史焦点提示, got: %s", bar2)
	}
}

// 焦点在历史时 ctrl+↑/ctrl+↓（输入区滚动键）也拦截为滚动历史，且保持焦点、
// 绝不滚动输入区（与"焦点在历史→所有滚动键滚动历史"语义一致）。
func TestFocusHistory_CtrlUpDown_ScrollsHistory(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = true
	before := m.scrollOffset

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlUp})
	m2 := res.(*model)
	if m2.scrollOffset <= before {
		t.Fatalf("焦点在历史时 ctrl+↑ 应滚动历史, before=%d after=%d", before, m2.scrollOffset)
	}
	if !m2.focusHistory {
		t.Fatalf("ctrl+↑ 后焦点应保持在历史区")
	}
	if m2.inputScroll != 0 {
		t.Fatalf("焦点在历史时 ctrl+↑ 不得滚动输入区, inputScroll=%d", m2.inputScroll)
	}
	before2 := m2.scrollOffset
	res2, _ := m2.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlDown})
	m3 := res2.(*model)
	if m3.scrollOffset >= before2 {
		t.Fatalf("焦点在历史时 ctrl+↓ 应向下滚动, before=%d after=%d", before2, m3.scrollOffset)
	}
	if m3.chatInput != "" {
		t.Fatalf("焦点在历史时 ctrl+↓ 不得修改输入框, chatInput=%q", m3.chatInput)
	}
}

// 点击边界：viewInputTop-1 → 历史区焦点；viewInputTop（分隔线）→ 输入区焦点；
// 状态栏（height-1）→ 输入区焦点。
func TestFocusHistory_ClickBoundary(t *testing.T) {
	m := focusTestModel(t)
	// 分隔线上方一行 → 历史区
	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: m.viewInputTop - 1})
	if !res.(*model).focusHistory {
		t.Fatalf("点击 viewInputTop-1=%d 应聚焦历史区", m.viewInputTop-1)
	}
	// 分隔线（输入区首行）→ 输入区
	res2, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: m.viewInputTop})
	if res2.(*model).focusHistory {
		t.Fatalf("点击输入区分隔线 viewInputTop=%d 应聚焦输入框", m.viewInputTop)
	}
	// 状态栏 → 输入区
	res3, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: m.height - 1})
	if res3.(*model).focusHistory {
		t.Fatalf("点击状态栏应聚焦输入框")
	}
}

// 焦点在输入框 + 光标在输入区 + 输入不可滚动（单行）→ 滚轮仍滚动消息历史（原行为保留）。
func TestFocusHistory_WheelOverInput_NotScrollable_ScrollsHistory(t *testing.T) {
	m := focusTestModel(t)
	m.focusHistory = false
	m.chatInput = "单行输入"
	m.cursor = len([]rune(m.chatInput))
	before := m.scrollOffset

	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonWheelUp, X: 5, Y: m.viewInputTop + 1})
	m2 := res.(*model)
	if m2.scrollOffset <= before {
		t.Fatalf("输入不可滚动时滚轮应滚动历史, before=%d after=%d", before, m2.scrollOffset)
	}
	if m2.inputScroll != 0 {
		t.Fatalf("输入不可滚动时滚轮不得滚动输入区, inputScroll=%d", m2.inputScroll)
	}
}

// 确认对话框可见时 viewInputTop 应精确指向输入区分隔线（已计入对话框覆盖层行数）。
func TestView_InputTop_WithDialog(t *testing.T) {
	m := focusTestModel(t)
	m.confirmDialog.Show(&tools.ConfirmDecision{
		Level:    tools.ConfirmNormal,
		ToolName: "exec_command",
		Reasons:  []string{"测试原因"},
	})
	out := m.View()
	lines := strings.Split(out, "\n")
	if m.viewInputTop < 0 || m.viewInputTop >= len(lines) {
		t.Fatalf("viewInputTop 越界: %d (lines=%d)", m.viewInputTop, len(lines))
	}
	if !strings.Contains(lines[m.viewInputTop], "─") {
		t.Fatalf("对话框可见时 viewInputTop=%d 行应为输入区分隔线, got %q", m.viewInputTop, lines[m.viewInputTop])
	}
}
