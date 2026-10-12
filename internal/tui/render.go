package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// maxHistoryLines 消息区历史渲染行数上限：超过时只保留最新行，
// 防止超长历史导致滚动查看卡顿与内存/渲染开销（"滚动 tui 查看历史卡顿"）。
const maxHistoryLines = 1000

func (m *model) View() string {
	if m.width < 20 || m.height < 5 {
		return "Loading..."
	}

	mainW := m.width - 2
	if mainW < 20 {
		mainW = 20
	}
	inputH := inputAreaHeight(m)
	panelH := subTaskPanelHeight(m)
	status := renderStatusBar(m)
	// 状态栏实际渲染行数：内容超宽时 lipgloss 自动折行可能占多行，
	// 必须动态计入预算，否则总行数超过终端高度，输入框被顶出可视区。
	statusLines := strings.Count(status, "\n") + 1

	// 命令建议面板：提前渲染一次（预算用行数 + 输出复用），其高度必须计入
	// 固定区预算——旧实现只在输出时追加、不占预算，总行数 = height + 面板行数
	// 超出终端高度，输入框被顶出可视区（与"帮助面板/状态栏折行"同类 bug）。
	// 渲染条件与输出处完全一致：命令模式 + 有建议 + 输入为纯命令名前缀。
	var suggPanel string
	if m.mode == modeCommand && len(m.completions) > 0 && !strings.Contains(m.input, " ") {
		suggPanel = renderSuggestions(m)
	}
	suggLines := 0
	if suggPanel != "" {
		suggLines = strings.Count(suggPanel, "\n") + 1
	}

	// 覆盖层行数（确认对话框；帮助面板并入 body 区域，不额外占行）
	extraLines := 0
	var dialog string
	if m.confirmDialog != nil && m.confirmDialog.IsVisible() {
		dialog = m.RenderConfirmDialog()
		extraLines = strings.Count(dialog, "\n") + 1
	}

	// 固定区（子任务面板 + 输入区 + 状态栏）；body 区 = 剩余行。
	// 任何覆盖层都计入高度预算，保证 View() 总行数恒 ≤ 终端高度，
	// 输入框/状态栏恒在底部可见。旧实现把帮助面板追加在输出末尾，
	// 导致总行数超过终端高度——Windows conhost 对超高输出滚动显示底部，
	// 输入框/状态栏被顶出可视区（"帮助信息残留导致输入框看不见"）。
	fixed := panelH + inputH + statusLines + suggLines
	bodyH := m.height - fixed - extraLines
	if bodyH < 1 {
		bodyH = 1
	}
	m.viewBodyH = bodyH // 保存给 handleMouse：鼠标事件对应本帧/上帧渲染结果
	// 输入区顶部行号（= height - 状态栏行数 - 输入区行数）：handleMouse 据此区分
	// 点击/悬停位置在历史区还是输入区，实现焦点路由（Windows 滚动冲突根治）。
	m.viewInputTop = m.height - statusLines - inputH - extraLines

	var allLines []string
	m.clickMap = make(map[int]int)
	for msgIdx, msg := range m.messages {
		// 渲染缓存：签名不变直接复用渲染行（滚动/重绘零 lipgloss 开销），
		// 只有内容变化（流式追加、折叠切换）才重新渲染——解决消息多时
		// 滚动查看历史卡顿（此前每帧对全部消息全量渲染）。
		var lines []string
		if c, ok := m.renderCache[msgIdx]; ok && c.signature == msgRenderSignature(msg, mainW) {
			lines = c.lines
		} else {
			rendered := renderMessage(msg, mainW, false)
			lines = strings.Split(rendered, "\n")
			m.renderCache[msgIdx] = &msgRenderCache{
				lines:     lines,
				signature: msgRenderSignature(msg, mainW),
			}
		}
		lineStart := len(allLines)
		allLines = append(allLines, lines...)
		// 记录思考消息所有行位置，点击任意行都能展开/折叠
		if msg.Type == MsgThinking {
			for li := lineStart; li < len(allLines); li++ {
				m.clickMap[li] = msgIdx
			}
		}
	}

	if m.mode == modeSkillSelect {
		// 技能选择模式：渲染技能列表，不显示消息
		allLines = nil
		m.clickMap = nil // 消息不在屏幕显示，思考块不可点击
		title := lipgloss.NewStyle().Foreground(clrAccent).Bold(true).Render("  📚 技能选择（方向键移动 · 空格选中 · 回车确认 · ESC取消）")
		allLines = append(allLines, title)
		searchLine := "  🔍 " + m.skillSearch + "▌"
		allLines = append(allLines, searchLine)
		allLines = append(allLines, "")
		for i, idx := range m.skillFiltered {
			if idx >= len(m.skillAll) {
				continue
			}
			name := m.skillAll[idx]
			checked := m.skillSelected[idx]
			cb := "[ ]"
			pre := "  "
			if checked {
				cb = "[✓]"
			}
			if i == m.skillCursor {
				pre = "▸ "
			}
			line := pre + cb + " " + name
			if i == m.skillCursor {
				allLines = append(allLines, lipgloss.NewStyle().Foreground(clrAccent).Bold(true).Render(line))
			} else {
				allLines = append(allLines, lipgloss.NewStyle().Foreground(clrFg).Render(line))
			}
		}
		if len(m.skillFiltered) == 0 {
			allLines = append(allLines, lipgloss.NewStyle().Foreground(clrDim).Render("  无匹配技能"))
		}
		// 底部提示
		allLines = append(allLines, "")
		hint := lipgloss.NewStyle().Foreground(clrDim).Render("  ↑↓选择 · 空格切换 · 回车确认 · ESC取消")
		allLines = append(allLines, hint)
	}

	if m.streaming {
		spinner := spinnerFrames[m.tick%len(spinnerFrames)]
		// 实时分离思考和正文
		displayBody, displayThinks := splitThinkTags(m.streamBuf)
		if displayBody != "" || len(displayThinks) > 0 {
			streamLine := lipgloss.NewStyle().Foreground(clrGreen).Bold(true).Render("● ContextLoom") + " " +
				lipgloss.NewStyle().Foreground(clrAccent).Render(spinner) + " tokens:" + itoa(m.tokenCount)
			allLines = append(allLines, streamLine)
			// 显示思考块（折叠状态）：识别到 thinking 开头即显示动效，内容为空也显示
			if len(displayThinks) > 0 {
				thinkLine := lipgloss.NewStyle().Foreground(clrPurple).Render("💭 思考中 " + spinner + " ...")
				allLines = append(allLines, thinkLine)
			}
			// 显示正文：长输出只渲染尾部 maxStreamRenderLines 行，避免每帧全量
			// markdown 渲染导致卡顿（完整内容流结束后在消息区可滚动查看）
			if displayBody != "" {
				renderBody := tailLines(displayBody, maxStreamRenderLines)
				for _, line := range strings.Split(prefixLines(renderMarkdown(renderBody, mainW-4), "  "), "\n") {
					allLines = append(allLines, line)
				}
				// 折叠提示放在末尾，确保在可见区域内
				if renderBody != displayBody {
					allLines = append(allLines, lipgloss.NewStyle().Foreground(clrDim).Render(
						"  … 上方内容已折叠（输出较长，流结束后可 ↑ 滚动查看）"))
				}
			}
		} else {
			// 思考中动效
			thinkLine := lipgloss.NewStyle().Foreground(clrPurple).Render("💭 思考中 ") +
				lipgloss.NewStyle().Foreground(clrAccent).Render(spinner)
			allLines = append(allLines, thinkLine)
		}
	}

	// 帮助面板：渲染进 body 区域（替代消息），输入框/状态栏恒在底部。
	// 与消息区共用 bodyH 截断 + scrollOffset 滚动，总行数不会超过终端高度。
	if m.showHelp {
		allLines = strings.Split(renderHelpPanel(m, mainW), "\n")
		m.clickMap = nil // 帮助模式无思考块可点击
	}

	// ── 历史行数上限 ──
	// 消息区渲染行数限制在 maxHistoryLines 行内：超出时只保留最新行，
	// 防止超长历史（数万行）导致滚动查看卡顿/内存占用/渲染开销。
	// clickMap 行号同步偏移：被截掉的行不可再点击（思考块折叠）。
	// 帮助/技能选择/流式内容追加在其后，不受影响（它们本身行数少）。
	if len(allLines) > maxHistoryLines {
		cut := len(allLines) - maxHistoryLines
		allLines = allLines[cut:]
		if len(m.clickMap) > 0 {
			shifted := make(map[int]int, len(m.clickMap))
			for li, msgIdx := range m.clickMap {
				if li >= cut {
					shifted[li-cut] = msgIdx
				}
			}
			m.clickMap = shifted
		}
	}

	// ── 超宽行兜底（输入框消失的真正元凶）──
	// 任何渲染分支（diff 行/标题行/MsgSystem/MsgError/lipgloss 样式注入）若产生
	// 超过终端宽度的行，终端会自动折行 → 实际显示行数 > len(allLines) 预算 →
	// View() 总输出行数超过终端高度 → 输入框/状态栏被顶出可视区（"运行中输入框
	// 消失"）。这里对每一行按显示宽度截断（保留 ANSI 样式码），保证总行数恒等于
	// 预算，输入框恒在底部。
	for i, line := range allLines {
		allLines[i] = truncateANSIWidth(line, mainW)
	}

	// Bubbletea 使用 alternate screen，终端原生滚动不可用
	// 必须截断到 bodyH 行，用 scrollOffset 控制查看历史
	totalLines := len(allLines)
	var visible []string
	if totalLines <= bodyH {
		m.viewStart = 0
		visible = allLines
	} else {
		// 默认显示最后 bodyH 行（最新内容）
		end := totalLines - m.scrollOffset
		if end > totalLines {
			end = totalLines
		}
		if end < bodyH {
			end = bodyH
		}
		start := end - bodyH
		if start < 0 {
			start = 0
		}
		m.viewStart = start // 保存给 handleMouse（上一帧窗口起始行）
		visible = allLines[start:end]
	}

	if len(visible) == 0 {
		visible = []string{
			"",
			lipgloss.NewStyle().Foreground(clrAccent).Bold(true).Render("  ContextLoom v2.1"),
			"",
			lipgloss.NewStyle().Foreground(clrDim).Render("  直接输入聊天 · / 命令 · Ctrl+C 清空输入（再按退出）"),
		}
	}

	// 填充到 bodyH 行，确保布局稳定不抖动
	for len(visible) < bodyH {
		visible = append(visible, "")
	}

	body := strings.Join(visible, "\n")
	inputArea := renderInputArea(m, inputH)

	// 滚动提示（占用最后一行 body 位置，不额外加行）
	if m.scrollOffset > 0 && totalLines > bodyH {
		hint := lipgloss.NewStyle().Foreground(clrDim).Render(
			fmt.Sprintf("↑ 历史滚动 %d/%d 行 — ↓ 回到底部", m.scrollOffset, totalLines))
		// 替换 body 最后一行
		if len(visible) > 0 {
			visible[len(visible)-1] = hint
			body = strings.Join(visible, "\n")
		}
	}

	// 组装输出：body → 命令建议 → 子任务面板 → 输入区 → 状态栏
	out := body
	// 命令建议弹窗在输入区上方（行数已计入 suggLines 预算，不会挤出输入框）。
	// 硬约束：仅命令模式 + 有建议 + 输入为纯命令名前缀（无空格=尚未选定）时才渲染。
	// 一旦输入含空格（命令已选定，正在输入参数）或已发送，绝不渲染，强制刷掉残留。
	if suggPanel != "" {
		out += "\n" + suggPanel
	}
	// 子任务面板（task 模式下展示 spawn_task 子 Agent 实时状态）
	if panel := renderTaskPanel(m, mainW); panel != "" {
		out += "\n" + panel
	}
	out += "\n" + inputArea + "\n" + status
	// 确认对话框覆盖层（行数已计入 extraLines/bodyH 预算，不会挤出输入框）
	if dialog != "" {
		out += "\n" + dialog
	}
	return out
}

// subTaskPanelHeight 子任务面板占用行数（无子任务时 0）
func subTaskPanelHeight(m *model) int {
	if len(m.subTasks) == 0 {
		return 0
	}
	return len(m.subTasks) + 1 // 标题行 + 每项一行
}

// renderTaskPanel 渲染子任务面板：标题行 + 各子任务状态行
// 布局：标题分隔线（含运行/完成/失败计数）+ 每个子任务一行
//   - ⚡ 🧑💻coder [CODE_AGENT] 任务摘要… → 最新输出…   （员工命中：显示员工名）
//   - ✔ sub-2 [TEST_AGENT] 任务摘要… 90                 （未命中：保持 subID）
func renderTaskPanel(m *model, width int) string {
	if len(m.subTasks) == 0 {
		return ""
	}
	// 统计状态
	running, done, failed := 0, 0, 0
	for _, st := range m.subTasks {
		switch st.Status {
		case "running":
			running++
		case "done":
			done++
		case "failed":
			failed++
		}
	}
	title := fmt.Sprintf("⚡ 子任务 · %d 进行 · %d 完成 · %d 失败", running, done, failed)
	titleLen := len([]rune(title)) + 5 // "── " + title + " " + dashes
	dashes := width - titleLen
	if dashes < 0 {
		dashes = 0
	}
	sepLine := lipgloss.NewStyle().Foreground(clrBorder).Render(
		"── " + title + " " + strings.Repeat("─", dashes))

	var lines []string
	lines = append(lines, sepLine)

	for _, st := range m.subTasks {
		var icon string
		var color lipgloss.Color
		switch st.Status {
		case "running":
			icon, color = "⚡", clrAccent
		case "done":
			icon, color = "✔", clrGreen
		case "failed":
			icon, color = "✗", clrRed
		default:
			icon, color = "·", clrDim
		}
		directive := st.Directive
		if directive == "" {
			directive = "CODE_AGENT"
		}
		// 身份显示：员工命中（M3）显示员工名（带工牌 icon）；未命中回退 subID
		who := st.ID
		if st.Employee != "" {
			who = "🧑💻" + st.Employee
		}
		taskPreview := truncateRunes(st.Task, 24)
		lastLine := truncateRunes(strings.TrimSpace(st.LastLine), 32)
		scoreStr := ""
		if st.Status == "done" {
			scoreStr = fmt.Sprintf(" 评分%.0f", st.Score)
		}
		rest := fmt.Sprintf(" %s [%s] %s", who, directive, taskPreview)
		if lastLine != "" {
			rest += " → " + lastLine
		}
		rest += scoreStr
		rest = truncateRunes(rest, width-2) // 留 2 给图标
		line := lipgloss.NewStyle().Foreground(color).Render(icon) + rest
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func inputTotalLines(m *model) int {
	if m.mode == modeCommand || m.mode == modeSkillSelect {
		return 0
	}
	maxW := m.width - 2
	if maxW < 10 {
		maxW = 10
	}
	return len(strings.Split(wordWrap(m.chatInput, maxW), "\n"))
}

func inputAreaHeight(m *model) int {
	// 命令/技能选择模式：固定2行
	if m.mode == modeCommand || m.mode == modeSkillSelect {
		return 2
	}
	// 聊天/normal 模式：动态高度（按 wordWrap 后的实际行数），至少3行，最多8行
	lines := inputTotalLines(m)
	if lines < 3 {
		lines = 3
	}
	if lines > 8 {
		lines = 8
	}
	return lines + 1 // +1 分隔线
}

func renderInputArea(m *model, maxLines int) string {
	sep := lipgloss.NewStyle().Foreground(clrBorder).Render(strings.Repeat("─", m.width-2))

	// 命令模式：显示 m.input
	if m.mode == modeCommand {
		if m.input == "" {
			return sep + "\n/▌"
		}
		r := []rune(m.input)
		pre := string(r[:m.cursor])
		post := string(r[m.cursor:])
		return sep + "\n" + pre + "▌" + post
	}

	// 技能选择模式：显示搜索输入
	if m.mode == modeSkillSelect {
		if m.skillSearch == "" {
			return sep + "\n🔍 ▌"
		}
		return sep + "\n🔍 " + m.skillSearch + "▌"
	}

	// 命令/技能选择模式：各自渲染（见上）；normal 模式也渲染聊天输入框（常驻不消失）
	if m.mode == modeCommand || m.mode == modeSkillSelect {
		return ""
	}

	// 聊天/normal 模式：多行输入 + 自动换行 + 正确光标（输入框常驻）
	maxW := m.width - 2
	if maxW < 10 {
		maxW = 10
	}
	wrapped := wordWrap(m.chatInput, maxW)
	splitLines := strings.Split(wrapped, "\n")

	// 计算光标所在的 visual 行
	plain := []rune(m.chatInput)
	cursorPlain := m.cursor
	if cursorPlain > len(plain) {
		cursorPlain = len(plain)
	}
	cursorVisualLine := 0
	pos := 0
	for vi, vline := range splitLines {
		vlen := len([]rune(vline))
		if pos+vlen >= cursorPlain {
			cursorVisualLine = vi
			break
		}
		pos += vlen
		// 只有原始文本中有 \n 才 +1，wordWrap 生成的换行不计入 plain 位置
		if pos < len(plain) && plain[pos] == '\n' {
			pos++
		}
		if vi == len(splitLines)-1 {
			cursorVisualLine = vi
		}
	}

	// 构建显示行，光标所在行插入 ▌
	var displayLines []string
	pos = 0
	for vi, vline := range splitLines {
		vlen := len([]rune(vline))
		if vi == cursorVisualLine {
			col := cursorPlain - pos
			if col > vlen {
				col = vlen
			}
			if col < 0 {
				col = 0
			}
			pre := string([]rune(vline)[:col])
			post := string([]rune(vline)[col:])
			displayLines = append(displayLines, pre+"▌"+post)
		} else {
			displayLines = append(displayLines, vline)
		}
		pos += vlen
		// 只有原始文本中有 \n 才 +1，wordWrap 生成的换行不计入 plain 位置
		if pos < len(plain) && plain[pos] == '\n' {
			pos++
		}
	}

	// 最少3行
	for len(displayLines) < 3 {
		displayLines = append(displayLines, "")
	}

	// 截断到可见行数
	contentMax := m.height - 5 // 预留分隔线+状态栏
	if contentMax < 3 {
		contentMax = 3
	}
	total := len(displayLines)
	if total > contentMax {
		start := 0
		if m.inputScroll > 0 {
			// 手动滚动模式：从 inputScroll 位置显示
			start = m.inputScroll
			if start > total-contentMax {
				start = total - contentMax
				m.inputScroll = start
			}
		} else {
			// 跟随光标模式
			if cursorVisualLine >= contentMax {
				start = cursorVisualLine - contentMax + 1
			}
		}
		displayLines = displayLines[start:]
		if len(displayLines) > contentMax {
			displayLines = displayLines[:contentMax]
		}
		// 滚动指示器（右侧显示行号/总数 + 快捷键提示）
		hintStyle := lipgloss.NewStyle().Foreground(clrDim)
		scrollHint := " " + itoa(start+1) + "-" + itoa(start+len(displayLines)) + "/" + itoa(total) + " "
		if m.inputScroll > 0 {
			scrollHint += "Ctrl+↓ 回底部"
		} else {
			scrollHint += "Ctrl+↑ 查看"
		}
		// 先按 maxW 截宽再加滚动提示，防止 hint 让该行超宽折行（折行会把
		// 输入区行数撑大，状态栏被顶出可视区）
		displayLines[len(displayLines)-1] = truncateANSIWidth(displayLines[len(displayLines)-1], maxW-len([]rune(scrollHint)))
		displayLines[len(displayLines)-1] += hintStyle.Render(scrollHint)
	}

	return sep + "\n" + strings.Join(displayLines, "\n")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := make([]byte, 0, 10)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func renderSuggestions(m *model) string {
	if m.mode != modeCommand || len(m.completions) == 0 {
		return ""
	}
	visCount := len(m.completions)
	if visCount > 5 {
		visCount = 5
	}
	start := m.complIdx - 2
	if start < 0 {
		start = 0
	}
	if start > len(m.completions)-visCount {
		start = len(m.completions) - visCount
	}
	if start < 0 {
		start = 0
	}

	w := m.width - 6
	if w < 30 {
		w = 30
	}
	var lines []string
	bg := lipgloss.NewStyle().Background(lipgloss.Color("#1A1C29")).Width(w)
	selBg := lipgloss.NewStyle().Background(lipgloss.Color("#3E4164")).Foreground(lipgloss.Color("#D6DEFF")).Width(w)
	dimFg := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7094"))

	for i := start; i < start+visCount && i < len(m.completions); i++ {
		e := m.completions[i]
		line := lipgloss.NewStyle().Foreground(lipgloss.Color("#82AAFF")).Bold(true).Render(e.Name)
		if e.Desc != "" {
			line += "  " + dimFg.Render(e.Desc)
		}
		if i == m.complIdx {
			lines = append(lines, selBg.Render(" ▸ "+line))
		} else {
			lines = append(lines, bg.Render("   "+line))
		}
	}
	if len(m.completions) > 5 {
		lines = append(lines, dimFg.Render(fmt.Sprintf("  ... 还有 %d 条", len(m.completions)-5)))
	}
	return strings.Join(lines, "\n")
}

func splitThinkTags(text string) (body string, thinks []string) {
	// 支持的思考标签格式：
	// 1. 模型层标记：<|reasoning|> ... <|/reasoning|>（deepseek.go 输出，避免与正文英文单词 thinking/response 误判）
	// 2. 尖括号格式：<thinking>/<think>（精确匹配，不要求词边界）
	// 3. 旧格式：thinking ... response（thinking 处于行首或空白后，兼容历史模型输出，
	//    与 runtime.StripThinking 保持同一识别规则，避免 "thinking用户" 泄漏到正文/历史）
	openTags := []struct {
		tag          string
		needBoundary bool
	}{
		{"<|reasoning|>", false},
		{"<thinking>", false},
		{"<think>", false},
		{"thinking", true},
	}
	closeTags := []struct {
		tag          string
		needBoundary bool
	}{
		{"<|/reasoning|>", false},
		{"</thinking>", false},
		{"</think>", false},
		{"response", true},
	}

	for {
		// 找最早出现的开标签
		start := -1
		openTag := ""
		for _, op := range openTags {
			if idx := indexTag(text, op.tag, op.needBoundary); idx != -1 && (start == -1 || idx < start) {
				start = idx
				openTag = op.tag
			}
		}
		if start == -1 {
			body += text
			return
		}
		body += text[:start]

		// 闭标签只在开标签之后搜索，避免全文误匹配
		rest := text[start+len(openTag):]
		end := -1
		closeTag := ""
		for _, cl := range closeTags {
			if idx := indexTag(rest, cl.tag, cl.needBoundary); idx != -1 && (end == -1 || idx < end) {
				end = idx
				closeTag = cl.tag
			}
		}
		if end == -1 || (closeTag == "response" && !legacyBlockOK(text, start, rest, end)) {
			if openTag == "thinking" {
				// 旧格式未闭合/不可靠：当作普通正文保留（与 StripThinking 一致），继续向后查找
				body += openTag
				text = rest
				continue
			}
			// 未闭合的标签思考块归入 thinks，不泄漏到正文
			thinks = append(thinks, rest)
			return
		}
		// 有效思考块：旧格式 thinking 的前导空白并入思考块（与 StripThinking 删除范围一致）
		if openTag == "thinking" {
			for len(body) > 0 {
				last := body[len(body)-1]
				if last == ' ' || last == '\t' || last == '\n' || last == '\r' {
					body = body[:len(body)-1]
				} else {
					break
				}
			}
		}
		thinks = append(thinks, rest[:end])
		text = rest[end+len(closeTag):]
	}
}

// legacyBlockOK 校验旧格式 thinking...response 块是否可靠：
// 同行（无换行）宽松接受；跨行时要求 thinking 与 response 都独占一行（行首），
// 避免正文中 "thinking 环节 ... response 处理" 等英文单词被误吞。
func legacyBlockOK(text string, start int, rest string, end int) bool {
	if !strings.Contains(rest[:end], "\n") {
		return true // 同行格式
	}
	// 跨行：thinking 行首 且 response 行首
	thinkLineStart := start == 0 || text[start-1] == '\n'
	respLineStart := end == 0 || rest[end-1] == '\n'
	return thinkLineStart && respLineStart
}

// indexTag 在 s 中查找 tag 的位置；needBoundary 时要求 tag 前面是空格/行首/字符串开头，
// 避免把路径中的 think/thinking 子串（如 /home/thinking_project）误判为思考标签。
func indexTag(s, tag string, needBoundary bool) int {
	from := 0
	for {
		idx := strings.Index(s[from:], tag)
		if idx == -1 {
			return -1
		}
		idx += from
		if needBoundary {
			if idx > 0 {
				prev := s[idx-1]
				if prev != ' ' && prev != '\n' && prev != '\t' && prev != '\r' {
					// 不在词边界 → 跳过继续找
					from = idx + 1
					continue
				}
			}
		}
		return idx
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// truncateANSIWidth 按终端显示宽度截断字符串，ANSI 样式码不占宽度且原样保留。
// 用于 View() 兜底：任何渲染分支产生超宽行时，终端自动折行会把实际显示行数
// 撑大（> len(allLines) 预算），输入框/状态栏被顶出可视区——"运行中输入框消失"
// 的真正元凶。截断点落在内容中时补 \x1b[0m 复位，避免未闭合样式污染后续行。
func truncateANSIWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	var out strings.Builder
	w := 0
	runes := []rune(s)
	truncated := false
	i := 0
	for i < len(runes) {
		r := runes[i]
		if r == '\x1b' {
			// ANSI 转义序列（SGR \x1b[...m / OSC \x1b]...BEL 或 ESC\）：不占宽度，原样保留
			if i+1 < len(runes) && (runes[i+1] == '[' || runes[i+1] == ']') {
				end := -1
				if runes[i+1] == '[' {
					for k := i + 2; k < len(runes); k++ {
						if runes[k] == 'm' {
							end = k
							break
						}
					}
				} else {
					for k := i + 2; k < len(runes); k++ {
						if runes[k] == '\x07' {
							end = k
							break
						}
						if runes[k] == '\x1b' && k+1 < len(runes) && runes[k+1] == '\\' {
							end = k + 1
							break
						}
					}
				}
				if end != -1 {
					out.WriteString(string(runes[i : end+1]))
					i = end + 1
					continue
				}
			}
			i++ // 孤立 ESC，丢弃
			continue
		}
		rw := runeDisplayWidth(r)
		if w+rw > maxW {
			truncated = true
			break
		}
		out.WriteRune(r)
		w += rw
		i++
	}
	if truncated && strings.Contains(out.String(), "\x1b[") {
		// 截断后补复位码，防止未闭合样式污染后续行（纯文本不追加）
		out.WriteString("\x1b[0m")
	}
	return out.String()
}

// msgRenderCache 单条消息的渲染行缓存：滚动/重绘时复用，
// 避免每帧对全部消息重复 lipgloss 渲染（消息多时滚动卡顿的根治）。
type msgRenderCache struct {
	lines     []string // 渲染行（不含 \n）
	signature string   // 内容签名，变化时自动失效
}

// msgRenderSignature 消息渲染签名：任何影响渲染的字段变化都会使缓存失效。
// 字符串拼接比 lipgloss 渲染便宜几个数量级，滚动时全部命中缓存。
// 必须包含渲染宽度 w：终端 resize 后同一内容按新宽度换行，行数变化，
// 若不失效则 clickMap 行号与实际显示错位（思考块点击失效的根因之一）。
func msgRenderSignature(msg ChatMessage, w int) string {
	return fmt.Sprintf("%d|%v|%v|%d|%s|%s|%s",
		msg.Type, msg.Collapsed, msg.Streaming, w, msg.ToolName, msg.Content, msg.ToolArgs)
}
