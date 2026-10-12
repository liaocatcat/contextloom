package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHandleKey_FiltersSGRMouseSeq 验证 handleKey 过滤终端透传的 SGR 鼠标
// 序列，防止污染输入框：
//   - 单个序列（带 ESC / 不带 ESC 两种形态）
//   - 连续鼠标事件合并形态 "[<65;72;17M[<65;82;15M"（中间夹 M，
//     整串 ^...$ 匹配会漏掉，必须子串扫描）
//   - 过滤优先级高于 confirm 对话框（乱码不得触发对话框按键）
func TestHandleKey_FiltersSGRMouseSeq(t *testing.T) {
	cases := []string{
		"\x1b[<65;72;17M",
		"[<65;72;17M",
		"\x1b[<0;65;72;15m",
		"[<0;65;72;15m",
		"[<65;72;17M[<65;82;15M", // 合并形态（用户实测）
		"\x1b[<65;72;17M[<65;82;15M",
	}
	for _, s := range cases {
		m := initialModel(nil)
		m.mode = modeChat
		m.chatInput = "prefix"
		m.cursor = len([]rune("prefix"))

		res, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
		mm := res.(*model)
		if mm.chatInput != "prefix" {
			t.Errorf("SGR 序列 %q 未被过滤, 输入框被污染为 %q", s, mm.chatInput)
		}
	}
}

// TestHandleKey_SGRFilterNotFalsePositive 确保正常输入不受影响。
func TestHandleKey_SGRFilterNotFalsePositive(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.cursor = 0

	res, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	mm := res.(*model)
	if mm.chatInput != "hello" {
		t.Errorf("正常输入被误过滤: got %q", mm.chatInput)
	}
}

// TestSGRMouseSeqRe_MergedForm 直接验证正则对合并形态的子串匹配。
func TestSGRMouseSeqRe_MergedForm(t *testing.T) {
	if !sgrMouseSeqRe.MatchString("[<65;72;17M[<65;82;15M") {
		t.Error("正则应命中合并形态 [<65;72;17M[<65;82;15M")
	}
	if !sgrMouseSeqRe.MatchString("\x1b[<65;72;17M[<65;82;15M") {
		t.Error("正则应命中带 ESC 的合并形态")
	}
	if !sgrMouseSeqRe.MatchString("\x1b[<65;72;17M") {
		t.Error("正则应命中带 ESC 的单序列")
	}
	if !sgrMouseSeqRe.MatchString("[<65;72;17M") {
		t.Error("正则应命中无 ESC 的单序列")
	}
	if sgrMouseSeqRe.MatchString("hello") {
		t.Error("正则不应命中普通文本")
	}
	// 确认 sgrMouseSeqRe 确实被 handleKey 使用（防死代码回归）
	if !strings.Contains(sgrMouseSeqRe.String(), `\[<[0-9]+`) {
		t.Error("正则模式异常")
	}
}

// TestAppInp_StripsSGRChunked 验证终端把 SGR 鼠标序列拆成小块/逐字符到达时
// （handleKey 整体正则对单字符不命中），appInp 逐块插入后会对整体做残留
// 清理，不会累积成 "[<65;72;17M" 乱码。
func TestAppInp_StripsSGRChunked(t *testing.T) {
	// 模拟终端逐字符/小块发送 "[<65;72;17M"（无 ESC，ESC 可能被剥离）
	chunks := []string{"[", "<", "6", "5", ";", "7", "2", ";", "1", "7", "M"}
	cur := ""
	cursor := 0
	for _, c := range chunks {
		appInp(&cur, &cursor, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c)})
	}
	if strings.Contains(cur, "[<") {
		t.Errorf("逐字符 SGR 残留未被清理, 输入框 = %q", cur)
	}
	// 未闭合前缀（终端停在 "[<65;72;17" 未发送 M/m）也必须清掉
	cur = ""
	cursor = 0
	for _, c := range []string{"[", "<", "6", "5", ";", "7", "2"} {
		appInp(&cur, &cursor, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c)})
	}
	if strings.Contains(cur, "[<") {
		t.Errorf("未闭合 SGR 前缀残留未被清理, 输入框 = %q", cur)
	}
	// 正常输入不受影响
	cur = "hello"
	cursor = 5
	appInp(&cur, &cursor, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" world")})
	if cur != "hello world" {
		t.Errorf("正常输入被误清理: %q", cur)
	}
}

// TestPaste_StripsSGRMerged 验证 SGR 序列作为 Paste 事件整体/合并到达时
// （"[<65;72;17M[<65;82;15M"），插入后整体清理，不残留。
func TestPaste_StripsSGRMerged(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "prefix"
	m.cursor = len([]rune("prefix"))

	// Paste 事件携带合并 SGR 序列
	_, _ = m.handleChat(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("[<65;72;17M[<65;82;15M"),
		Paste: true,
	})
	if m.chatInput != "prefix" {
		t.Errorf("Paste 合并 SGR 残留未被清理, 输入框 = %q", m.chatInput)
	}

	// 粘贴内容中混有 SGR（鼠标事件穿插在正常粘贴中）
	_, _ = m.handleChat(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("abc[<65;72;17Mdef"),
		Paste: true,
	})
	if m.chatInput != "prefixabcdef" {
		t.Errorf("混合 SGR 未清理干净, 输入框 = %q", m.chatInput)
	}
}
