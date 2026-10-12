package tui

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
)

var (
	clrBg     = lipgloss.Color("#0F111A")
	clrFg     = lipgloss.Color("#D6DEFF")
	clrDim    = lipgloss.Color("#6B7094")
	clrAccent = lipgloss.Color("#82AAFF")
	clrGreen  = lipgloss.Color("#C3E88D")
	clrRed    = lipgloss.Color("#FF5370")
	clrYellow = lipgloss.Color("#FFCB6B")
	clrPurple = lipgloss.Color("#C792EA")
	clrCyan   = lipgloss.Color("#89DDFF")
	clrBorder = lipgloss.Color("#2E3047")
	clrStatus = lipgloss.Color("#181A26")

	cFg     = clrFg
	cDim    = clrDim
	cAccent = clrAccent
	cGreen  = clrGreen
	cRed    = clrRed
	cYellow = clrYellow
	cPurple = clrPurple
	cCyan   = clrCyan
	cBg     = clrBg
	cBorder = clrBorder
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

var (
	styleBase   = lipgloss.NewStyle().Foreground(clrFg)
	styleDim    = lipgloss.NewStyle().Foreground(clrDim)
	styleUser   = lipgloss.NewStyle().Foreground(clrCyan).Bold(true)
	styleAI     = lipgloss.NewStyle().Foreground(clrGreen).Bold(true)
	styleThink  = lipgloss.NewStyle().Foreground(clrPurple).Italic(true)
	styleTool   = lipgloss.NewStyle().Foreground(clrYellow)
	styleToolOK = lipgloss.NewStyle().Foreground(clrGreen)
	styleToolNO = lipgloss.NewStyle().Foreground(clrRed)
	styleErr    = lipgloss.NewStyle().Foreground(clrRed).Bold(true)
	styleBar    = lipgloss.NewStyle().Background(clrStatus).Foreground(clrFg)
	styleHelp   = lipgloss.NewStyle().Foreground(clrDim)
	styleSel    = lipgloss.NewStyle().Foreground(clrAccent)
	styleDiv    = lipgloss.NewStyle().Foreground(clrBorder)

	baseStyle       = styleBase
	dimStyle        = styleDim
	UserStyle       = styleUser
	BodyStyle       = styleBase
	SystemStyle     = styleDim
	ThinkingStyle   = styleThink
	ToolCallStyle   = styleTool
	ToolResultStyle = styleToolOK
	ToolErrorStyle  = styleToolNO
	ErrorStyle      = styleErr
	StatusBarStyle  = styleBar
)

func renderMessage(msg ChatMessage, maxW int, sel bool) string {
	w := maxW - 4
	if w < 10 {
		w = 10
	}
	mark := "  "
	if sel {
		mark = styleSel.Render("\u25cf ")
	}

	switch msg.Type {
	case MsgSystem:
		// 超宽内容先截断再渲染：lipgloss Width(w) 只设最小宽度不截断，
		// 超宽行会被终端折行撑大 View() 总行数，顶掉输入框/状态栏。
		// 必须逐行截断：多行清单类命令结果（/emp 员工列表、/model 模型列表）
		// 若整串按 rune 截断，会在第一行超宽处截掉后续全部行——表现为
		// "总数 6 名却只显示 2 条"（用户反馈根因）。
		return styleDim.Width(w).Render(mark + truncMsgLines(msg.Content, w-2))
	case MsgUser:
		ts := ""
		if !msg.Timestamp.IsZero() {
			ts = lipgloss.NewStyle().Foreground(cDim).Render(" " + msg.Timestamp.Format("15:04"))
		}
		return styleUser.Render(mark+"\u25b8 \u4f60"+ts) + "\n" + prefixLines(renderMarkdown(msg.Content, w-2), "  ")
	case MsgAssistant:
		ts := ""
		if !msg.Timestamp.IsZero() {
			ts = lipgloss.NewStyle().Foreground(cDim).Render(" " + msg.Timestamp.Format("15:04"))
		}
		return styleAI.Render(mark+"\u25cf ContextLoom"+ts) + "\n" + prefixLines(renderMarkdown(msg.Content, w-2), "  ")
	case MsgThinking:
		if msg.Collapsed {
			if msg.Streaming {
				i := len(msg.Content)
				frame := spinnerFrames[i%len(spinnerFrames)]
				return styleThink.Render(mark + "\U0001f4ad \u601d\u8003\u4e2d " + frame + " ...")
			}
			return styleThink.Render(mark + "\U0001f4ad \u601d\u8003\u5b8c\u6210 \u00b7 \u70b9\u51fb\u5c55\u5f00")
		}
		return styleThink.Render(mark+"\U0001f4ad \u601d\u8003\u8fc7\u7a0b") + "\n" + styleDim.Width(w).Render("  "+wordWrap(msg.Content, w-2))
	case MsgToolCall:
		return styleTool.Render(mark+"\u26a1 "+msg.ToolName) + "  " + styleDim.Render(truncMsg(msg.ToolArgs, w-10))
	case MsgToolResult:
		return styleToolOK.Render(mark+"\u2714 "+msg.ToolName) + "  " + styleDim.Render(truncMsg(msg.Content, min3(w-10, 100)))
	case MsgToolError:
		return styleToolNO.Render(mark+"\u2717 "+msg.ToolName) + "  " + styleErr.Render(truncMsg(msg.Content, min3(w-10, 100)))
	case MsgError:
		// 同上：错误消息可能包含超长工具日志/路径，超宽折行会把输入框顶出可视区
		return styleErr.Render(mark + "\u2717 " + truncMsg(msg.Content, w-4))
	default:
		return ""
	}
}

func fmtInput(s string, cursor, maxW int) string {
	if maxW < 10 {
		maxW = 10
	}
	r := []rune(s)
	if len(r) <= maxW {
		return string(r[:cursor]) + "\u258c" + string(r[cursor:])
	}
	half := maxW / 2
	start := cursor - half
	if start < 0 {
		start = 0
	}
	end := start + maxW
	if end > len(r) {
		end = len(r)
		start = end - maxW
		if start < 0 {
			start = 0
		}
	}
	return string(r[start:cursor]) + "\u258c" + string(r[cursor:end])
}

func renderStatusBar(m *model) string {
	modeLabel := "\U0001f4ac chat"
	if m.rt != nil {
		switch m.rt.GetMode() {
		case "task":
			modeLabel = "\u26a1 task"
		case "goal":
			modeLabel = "\U0001f3af goal"
		}
	}
	left := fmt.Sprintf(" \u6a21\u578b:%s \u00b7 %s \u00b7 \u6df1\u5ea6:%s \u00b7 \u9608\u503c:%.0f  ", m.modelID, modeLabel, m.reasoningEffort, m.threshold)
	var right string
	switch m.mode {
	case modeChat:
		if m.focusHistory {
			right = "\u7126\u70b9:\u5386\u53f2\u533a \u00b7 \u2191\u2193\u6eda\u52a8\u5386\u53f2 \u00b7 \u4efb\u610f\u952e\u8fd4\u56de\u8f93\u5165"
		} else {
			right = "\u8f93\u5165\u533a\u5df2\u6fc0\u6d3b \u00b7 Enter \u53d1\u9001 \u00b7 " + m.newlineKeyLabel() + " \u6362\u884c \u00b7 ? \u5e2e\u52a9"
		}
	case modeCommand:
		right = "\u547d\u4ee4\u6a21\u5f0f"
		if len(m.completions) > 0 {
			right += fmt.Sprintf(" [%d\u4e2a\u5efa\u8bae]", len(m.completions))
		}
	case modeSkillSelect:
		right = "\u6280\u80fd\u9009\u62e9\u6a21\u5f0f"
		if m.rt != nil {
			skills := m.rt.ListSkills()
			right = fmt.Sprintf("\u6280\u80fd\u9009\u62e9 (%d\u53ef\u9009)", len(skills))
		}
	default:
		if m.streaming {
			right = spinnerFrames[m.tick%len(spinnerFrames)] + " \u5904\u7406\u4e2d\u2026 tokens:" + itoa(m.tokenCount)
		} else {
			right = "Enter \u804a\u5929 \u00b7 Space \u5c55\u5f00 \u00b7 / \u547d\u4ee4 \u00b7 Ctrl+C \u6e05\u7a7a\u518d\u9000"
		}
	}
	// 上下文预算用量展示（始终显示——压缩发生在流式处理期间，
	// 若只在非 streaming 时显示，用户将看不到压缩后的实时占比变化）
	if m.rt != nil {
		if usage := m.rt.ContextUsage(); usage != "" {
			if right != "" {
				right += " \u00b7 "
			}
			right += usage
		}
	}
	used := lipgloss.Width(left) + lipgloss.Width(right)
	mid := ""
	if m.width > used+4 {
		mid = strings.Repeat(" ", m.width-used-2)
	}
	return styleBar.Width(m.width).Render(left + mid + right + " ")
}

// renderHelpPanel \u5e2e\u52a9\u9762\u677f\uff08? \u952e\u6253\u5f00\uff09\uff1a\u5217\u51fa\u5168\u90e8\u5feb\u6377\u952e\u4e0e\u7ec8\u7aef\u517c\u5bb9\u6027\u63d0\u793a
func renderHelpPanel(m *model, width int) string {
	if width < 46 {
		width = 46
	}
	nk := m.newlineKeyLabel()
	rows := [][2]string{
		{"Enter", "\u53d1\u9001\u6d88\u606f"},
		{nk, "\u63d2\u5165\u6362\u884c\uff08\u591a\u884c\u8f93\u5165\uff09"},
		{`\` + " + Enter", "\u8f6f\u6362\u884c\uff08\u884c\u5c3e \\ \u56de\u8f66\uff09"},
		{"Ctrl+A / Home", "\u5149\u6807\u5230\u884c\u9996"},
		{"Ctrl+E / End", "\u5149\u6807\u5230\u884c\u5c3e"},
		{"Ctrl+U", "\u5220\u9664\u5230\u884c\u9996"},
		{"Ctrl+K", "\u5220\u9664\u5230\u884c\u5c3e"},
		{"Ctrl+W", "\u5220\u9664\u524d\u4e00\u4e2a\u8bcd"},
		{"\u2191 / \u2193", "\u591a\u884c\u5185\u79fb\u52a8\u5149\u6807\uff08\u9996\u884c\u2191/\u672b\u884c\u2193 \u5207\u5386\u53f2\uff09"},
		{"Ctrl+\u2191 / \u2193", "\u8f93\u5165\u533a\u6eda\u52a8"},
		{"PgUp / PgDown", "\u6eda\u52a8\u67e5\u770b\u5386\u53f2\u6d88\u606f"},
		{"t", "\u5c55\u5f00/\u6298\u53e0\u601d\u8003\u5757\uff08\u952e\u76d8\u66ff\u4ee3\uff09"},
		{"\u9f20\u6807\u5de6\u952e", "\u70b9\u51fb\u5c55\u5f00/\u6298\u53e0\u601d\u8003\u5757"},
		{"\u70b9\u51fb\u5386\u53f2\u533a\u540e\u2191\u2193/\u6eda\u8f6e", "\u6eda\u52a8\u5386\u53f2\uff1b\u4efb\u610f\u952e\u8fd4\u56de\u8f93\u5165\u6846"},
		{"Shift+\u62d6\u9009", "Windows \u590d\u5236\u6587\u672c\uff08cmd \u4e0b\u7528\u53f3\u952e\u2192\u6807\u8bb0\uff09"},
		{"?", "\u5173\u95ed\u672c\u9762\u677f"},
		{"ESC", "\u5173\u95ed\u672c\u9762\u677f"},
	}
	innerW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r[0]); w > innerW {
			innerW = w
		}
	}
	bodyW := width - 6
	title := "\u5feb\u6377\u952e"
	top := "\u250c\u2500 " + title + " \u2500" + strings.Repeat("\u2500", bodyW-len([]rune(title))-3) + "\u2510"
	var lines []string
	lines = append(lines, top)
	for _, r := range rows {
		key := lipgloss.NewStyle().Foreground(clrAccent).Bold(true).Render(r[0])
		pad := innerW - lipgloss.Width(r[0]) + 3
		if pad < 1 {
			pad = 1
		}
		desc := lipgloss.NewStyle().Foreground(clrFg).Render(r[1])
		line := "\u2502 " + key + strings.Repeat(" ", pad) + desc + strings.Repeat(" ", bodyW-innerW-lipgloss.Width(r[1])-3) + "\u2502"
		if lipgloss.Width(line) < bodyW+2 {
			line += strings.Repeat(" ", bodyW+2-lipgloss.Width(line))
		}
		lines = append(lines, line)
	}
	lines = append(lines, "\u2514"+strings.Repeat("\u2500", bodyW)+"\u2518")
	// \u7ec8\u7aef\u517c\u5bb9\u6027\u63d0\u793a\uff08\u68c0\u6d4b\u5230\u5360\u7528\u51b2\u7a81\u65f6\u5c55\u793a\uff09
	if m.termHint != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(clrYellow).Render("\u26a0 "+m.termHint))
	}
	lines = append(lines, lipgloss.NewStyle().Foreground(clrDim).Render("  \u4efb\u610f\u952e\u5173\u95ed\u672c\u9762\u677f"))
	return lipgloss.NewStyle().Foreground(clrBorder).Render(strings.Join(lines, "\n"))
}

// wordWrap \u6309\u7ec8\u7aef display width \u6362\u884c\uff08CJK \u5bbd\u5b57\u7b26 = 2\uff0cASCII = 1\uff09
func wordWrap(s string, maxW int) string {
	if maxW < 8 {
		return s
	}
	var lines []string
	for len(s) > 0 {
		if strDisplayWidth(s) <= maxW {
			lines = append(lines, s)
			break
		}
		runes := []rune(s)
		br := 0
		w := 0
		lastSpace := -1
		for i, r := range runes {
			rw := runeDisplayWidth(r)
			if w+rw > maxW {
				if lastSpace > 0 {
					br = lastSpace
				} else {
					br = i
				}
				break
			}
			w += rw
			br = i + 1
			if r == ' ' {
				lastSpace = br
			}
		}
		if br == 0 {
			br = 1
		}
		lines = append(lines, string(runes[:br]))
		if br < len(runes) && runes[br] == ' ' {
			br++
		}
		s = string(runes[br:])
	}
	return strings.Join(lines, "\n")
}

func strDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

func runeDisplayWidth(r rune) int {
	if r == '\t' {
		return 4
	}
	if r == '\n' || r == '\r' {
		return 0
	}
	if unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hangul, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		(r >= 0xFF01 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) {
		return 2
	}
	return 1
}

func truncMsg(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}

// truncMsgLines 对多行文本逐行截断（每行不超过 n 个 rune），保留全部行。
// 单行文本退化为 truncMsg。用于系统消息（/emp、/model 等清单输出）：
// 整串截断会丢掉超宽行之后的所有行，逐行截断保证行数完整。
func truncMsgLines(s string, n int) string {
	if !strings.Contains(s, "\n") {
		return truncMsg(s, n)
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = truncMsg(l, n)
	}
	return strings.Join(lines, "\n")
}

func renderMarkdown(text string, maxW int) string {
	if maxW < 20 {
		maxW = 20
	}
	var result strings.Builder
	lines := strings.Split(text, "\n")
	inCodeBlock := false
	for _, line := range lines {
		if strings.HasPrefix(line, "\x60\x60\x60") {
			inCodeBlock = !inCodeBlock
			if inCodeBlock {
				lang := strings.TrimPrefix(line, "\x60\x60\x60")
				result.WriteString(lipgloss.NewStyle().Foreground(cDim).Render("\u250c\u2500 "+lang) + "\n")
			} else {
				result.WriteString(lipgloss.NewStyle().Foreground(cDim).Render("\u2514\u2500") + "\n")
			}
			continue
		}
		if inCodeBlock {
			wrapped := wordWrap(line, maxW-4)
			for _, wl := range strings.Split(wrapped, "\n") {
				result.WriteString(lipgloss.NewStyle().Foreground(cYellow).Render("\u2502 "+wl) + "\n")
			}
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "+ ") {
			result.WriteString(lipgloss.NewStyle().Foreground(cGreen).Render(line) + "\n")
			continue
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") && !strings.HasPrefix(line, "- ") {
			result.WriteString(lipgloss.NewStyle().Foreground(cRed).Render(line) + "\n")
			continue
		}
		if strings.HasPrefix(line, "@@") {
			result.WriteString(lipgloss.NewStyle().Foreground(cAccent).Render(line) + "\n")
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "# ") {
			result.WriteString(lipgloss.NewStyle().Foreground(cAccent).Bold(true).Render(line) + "\n")
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "+ ") {
			runes := []rune(trimmed)
			content := string(runes[2:])
			wrapped := wordWrap(content, maxW-4)
			wrappedLines := strings.Split(wrapped, "\n")
			for i, wl := range wrappedLines {
				if i == 0 {
					result.WriteString(lipgloss.NewStyle().Foreground(cAccent).Render("\u2022") + " " + wl + "\n")
				} else {
					result.WriteString("  " + wl + "\n")
				}
			}
			continue
		}
		wrapped := wordWrap(line, maxW)
		for _, wl := range strings.Split(wrapped, "\n") {
			rendered := wl
			for {
				start := strings.Index(rendered, "\x60")
				if start == -1 {
					break
				}
				end := strings.Index(rendered[start+1:], "\x60")
				if end == -1 {
					break
				}
				end += start + 1
				rendered = rendered[:start] + lipgloss.NewStyle().Foreground(cYellow).Background(lipgloss.Color("#2A2D3E")).Render(rendered[start+1:end]) + rendered[end+1:]
			}
			for {
				start := strings.Index(rendered, "**")
				if start == -1 {
					break
				}
				end := strings.Index(rendered[start+2:], "**")
				if end == -1 {
					break
				}
				end += start + 2
				rendered = rendered[:start] + lipgloss.NewStyle().Bold(true).Render(rendered[start+2:end]) + rendered[end+2:]
			}
			result.WriteString(rendered + "\n")
		}
	}
	return strings.TrimRight(result.String(), "\n")
}

func prefixLines(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func min3(a, b int) int {
	if a < b {
		return a
	}
	return b
}
