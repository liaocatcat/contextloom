package tui

import (
	"strings"
	"testing"

	bubbletea "github.com/charmbracelet/bubbletea"
)

// Bug: tui 运行中思考完成后，鼠标点击无法展开/折叠思考块。
// 根因：handleMouse 自行重算 bodyH（硬编码状态栏 1 行、漏算对话框行数）
// 与 View() 实际布局不一致 → 点击坐标错位 1 行以上，折叠的思考块（单行）
// 点击命中的是上方普通消息 → clickMap 查不到 → 点击无效。
// 修复：handleMouse 直接复用上一帧 View() 计算的 viewBodyH/viewStart。
func TestHandleMouse_ClickTogglesThinking(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.width, m.height = 100, 30
	m.messages = []ChatMessage{
		asstMsg("普通回复一"),
		{Type: MsgThinking, Content: "思考内容一", Collapsed: true, Streaming: false},
		asstMsg("普通回复二"),
		{Type: MsgThinking, Content: "思考内容二", Collapsed: true, Streaming: false},
	}
	m.renderCache = make(map[int]*msgRenderCache)

	// 先渲染一帧：viewBodyH/viewStart/clickMap 就位
	out := m.View()
	if !strings.Contains(out, "思考完成") {
		t.Fatalf("View 应显示折叠的思考块（思考完成·点击展开），实际输出:\n%s", out)
	}
	if m.viewBodyH <= 0 {
		t.Fatalf("viewBodyH 未设置: %d", m.viewBodyH)
	}
	// 定位"思考完成"提示在 body 中的行号（body 是输出的前 viewBodyH 行）
	lines := strings.Split(out, "\n")
	clickLine := -1
	for i := 0; i < m.viewBodyH && i < len(lines); i++ {
		if strings.Contains(lines[i], "思考完成") {
			clickLine = i
			break
		}
	}
	if clickLine < 0 {
		t.Fatalf("body 中未找到思考块行（viewBodyH=%d），输出:\n%s", m.viewBodyH, out)
	}

	// 点击思考块行 → 应展开（Collapsed: true → false）
	click := bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: clickLine}
	res, _ := m.handleMouse(click)
	m2 := res.(*model)
	found := false
	for _, msg := range m2.messages {
		if msg.Type == MsgThinking && !msg.Collapsed {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("BUG: 点击思考块行(%d)未展开任何思考块。messages=%+v", clickLine, m2.messages)
	}
	// 渲染缓存应已失效（下次 View 重新渲染展开态）
	if len(m2.renderCache) >= len(m2.messages) {
		t.Errorf("点击后思考消息的渲染缓存应失效")
	}

	// 渲染新一帧（展开态），再点击应折叠
	out2 := m2.View()
	if !strings.Contains(out2, "思考过程") {
		t.Fatalf("展开后 View 应显示思考过程，实际输出:\n%s", out2)
	}
	lines2 := strings.Split(out2, "\n")
	clickLine2 := -1
	for i := 0; i < m2.viewBodyH && i < len(lines2); i++ {
		if strings.Contains(lines2[i], "思考过程") {
			clickLine2 = i
			break
		}
	}
	if clickLine2 < 0 {
		t.Fatalf("展开后未找到思考块标题行，输出:\n%s", out2)
	}
	res2, _ := m2.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: clickLine2})
	m3 := res2.(*model)
	for _, msg := range m3.messages {
		if msg.Type == MsgThinking && !msg.Collapsed {
			t.Fatalf("BUG: 再次点击后思考块仍为展开态（未折叠）")
		}
	}
}

// Bug: 状态栏实际渲染 2 行（状态行 + ctx 用量行）时，旧 handleMouse 用
// `height - inputH - panelH - 1` 重算 bodyH 比 View() 大 1 行，
// startLine 随之错位 1 行 → 点击思考块命中的是上方行。
// 修复后 handleMouse 直接复用上一帧 viewStart，不再自行重算。
func TestHandleMouse_UsesViewStart_NoRecompute(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.width, m.height = 100, 40
	m.messages = []ChatMessage{
		{Type: MsgThinking, Content: "目标思考块", Collapsed: true, Streaming: false},
		asstMsg("第二行"),
		asstMsg("第三行"),
	}
	m.renderCache = make(map[int]*msgRenderCache)

	// 模拟上一帧 View() 计算结果：状态栏 2 行 → 消息区高度 26 行，
	// 窗口起始全局行号 2（思考块全局行 2 位于可见窗口第 0 行）。
	// 旧算法: startLine = totalLines(3) - (40-inputH-panelH-1) - 0，必 ≠ 2。
	m.viewBodyH = 26
	m.viewStart = 2
	m.clickMap = map[int]int{2: 0} // 全局行 2 = 思考块首行

	// 点击可见窗口第 0 行（全局行 = viewStart + 0 = 2）→ 应命中思考块
	res, _ := m.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: 0})
	m2 := res.(*model)
	if m2.messages[0].Collapsed {
		t.Fatalf("BUG: 点击应展开思考块（viewStart=2 命中全局行 2），实际仍折叠")
	}

	// 再次点击（新一帧布局：展开后思考块 2 行，viewStart 不变）→ 应折叠
	res2, _ := m2.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: 0})
	m3 := res2.(*model)
	if !m3.messages[0].Collapsed {
		t.Fatalf("BUG: 再次点击应折叠思考块，实际仍展开")
	}

	// 点击窗口外（Y >= viewBodyH）→ 必须忽略，不越界不 panic
	res3, _ := m3.handleMouse(bubbletea.MouseMsg{Button: bubbletea.MouseButtonLeft, Action: bubbletea.MouseActionPress, X: 5, Y: 26})
	m4 := res3.(*model)
	if !m4.messages[0].Collapsed {
		t.Fatalf("窗口外点击不应影响思考块（应保持折叠）")
	}
}

// Bug: 终端 resize 后同一消息按新宽度换行、行数变化，渲染缓存若不失效
// 则 clickMap 行号与实际显示错位（思考块点击失效的次因）。
func TestMsgRenderSignature_IncludesWidth(t *testing.T) {
	msg := ChatMessage{Type: MsgThinking, Content: strings.Repeat("很长的思考内容", 20), Collapsed: false, Streaming: false}
	if msgRenderSignature(msg, 80) == msgRenderSignature(msg, 100) {
		t.Fatalf("BUG: 渲染宽度不同时签名应不同（resize 后缓存必须失效）")
	}
	// 同宽度同内容 → 签名一致（缓存命中）
	if msgRenderSignature(msg, 80) != msgRenderSignature(msg, 80) {
		t.Fatalf("同宽度同内容签名应一致")
	}
	// 折叠状态变化 → 签名变化
	msg2 := msg
	msg2.Collapsed = true
	if msgRenderSignature(msg, 80) == msgRenderSignature(msg2, 80) {
		t.Fatalf("折叠状态变化签名应不同")
	}
}
