package tui

import (
	"fmt"
	"strings"
	"testing"
)

func TestChatMessage_Types(t *testing.T) {
	u := usrMsg("hello")
	if u.Type != MsgUser || u.Content != "hello" {
		t.Error("usrMsg")
	}
	a := asstMsg("response")
	if a.Type != MsgAssistant {
		t.Error("asstMsg")
	}
	th := thinkMsg()
	if th.Type != MsgThinking || th.Collapsed {
		t.Error("thinkMsg: should be collapsed")
	}
	tc := toolCall("read", `{"path":"x"}`)
	if tc.Type != MsgToolCall || tc.ToolName != "read" {
		t.Error("toolCall")
	}
}

func TestChatMessage_AllConstructors(t *testing.T) {
	// sysMsg
	sys := sysMsg("system message")
	if sys.Type != MsgSystem || sys.Content != "system message" {
		t.Error("sysMsg")
	}
	if sys.Timestamp.IsZero() {
		t.Error("sysMsg should have timestamp")
	}

	// usrMsg
	usr := usrMsg("user input")
	if usr.Type != MsgUser || usr.Content != "user input" {
		t.Error("usrMsg")
	}

	// asstMsg
	asst := asstMsg("assistant response")
	if asst.Type != MsgAssistant || asst.Content != "assistant response" {
		t.Error("asstMsg")
	}

	// thinkMsg
	th := thinkMsg()
	if th.Type != MsgThinking || th.Collapsed || !th.Streaming {
		t.Error("thinkMsg: expected Collapsed=false, Streaming=true")
	}

	// toolCall
	tc := toolCall("search", `{"pattern":"hello"}`)
	if tc.Type != MsgToolCall || tc.ToolName != "search" || tc.ToolArgs != `{"pattern":"hello"}` {
		t.Error("toolCall")
	}

	// toolOK
	tok := toolOK("search", "found 5 results")
	if tok.Type != MsgToolResult || tok.ToolName != "search" || tok.Content != "found 5 results" {
		t.Error("toolOK")
	}

	// toolFail
	tf := toolFail("search", "permission denied")
	if tf.Type != MsgToolError || tf.ToolName != "search" || tf.Content != "permission denied" {
		t.Error("toolFail")
	}

	// errMsg
	er := errMsg("fatal error")
	if er.Type != MsgError || er.Content != "fatal error" {
		t.Error("errMsg")
	}
}

func TestChatMessage_Clone(t *testing.T) {
	orig := usrMsg("hello")
	cloned := orig.clone()
	if cloned.Content != orig.Content || cloned.Type != orig.Type {
		t.Error("clone should produce equal message")
	}
	// Verify it's a copy, not same pointer
	cloned.Content = "modified"
	if orig.Content == "modified" {
		t.Error("clone should be a copy")
	}
}

// ============================================================================
// RenderMessage 测试
// ============================================================================

func TestRenderMessage_Basic(t *testing.T) {
	r := renderMessage(usrMsg("hi"), 80, false)
	if r == "" {
		t.Error("empty render")
	}
}

func TestRenderMessage_ThinkingCollapsed(t *testing.T) {
	msg := thinkMsg()
	r := renderMessage(msg, 80, false)
	if msg.Collapsed && len(r) == 0 {
		t.Error("collapsed thinking should render hint")
	}
}

func TestRenderMessage_AllTypes(t *testing.T) {
	tests := []struct {
		name string
		msg  ChatMessage
	}{
		{"System", sysMsg("system info")},
		{"User", usrMsg("user text")},
		{"Assistant", asstMsg("AI response")},
		{"Thinking", thinkMsg()},
		{"ToolCall", toolCall("read", `{"path":"/tmp"}`)},
		{"ToolResult", toolOK("read", "file content")},
		{"ToolError", toolFail("write", "permission denied")},
		{"Error", errMsg("something went wrong")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := renderMessage(tt.msg, 80, false)
			if r == "" {
				t.Errorf("renderMessage for %s returned empty", tt.name)
			}
		})
	}
}

func TestRenderMessage_ExpandedThinking(t *testing.T) {
	msg := thinkMsg()
	msg.Collapsed = false
	msg.Streaming = false
	msg.Content = "Let me analyze this step by step..."
	r := renderMessage(msg, 80, false)
	if !strings.Contains(r, "Let me analyze") {
		t.Errorf("expanded thinking should show content, got: %s", r)
	}
}

func TestRenderMessage_StreamingThinking(t *testing.T) {
	msg := thinkMsg()
	msg.Collapsed = true
	msg.Streaming = true
	r := renderMessage(msg, 80, false)
	if !strings.Contains(r, "思考中") {
		t.Errorf("streaming thinking should show spinner hint, got: %s", r)
	}
}

func TestRenderMessage_Selected(t *testing.T) {
	r := renderMessage(usrMsg("test"), 80, true)
	if !strings.Contains(r, "●") {
		t.Error("selected message should show selection marker")
	}
}

func TestRenderMessage_NarrowWidth(t *testing.T) {
	// Very narrow width
	r := renderMessage(usrMsg("hello world"), 5, false)
	if r == "" {
		t.Error("should not return empty with narrow width")
	}
}

// ============================================================================
// renderStatusBar：上下文占比实时显示（压缩发生在流式期间，streaming 时也必须显示）
// ============================================================================

func TestRenderStatusBar_ShowsCtxWhileStreaming(t *testing.T) {
	rt := newStatusBarRuntime(t)
	m := initialModel(rt) // 先建会话（NewSession 会清空历史，与真实启动顺序一致）
	// 再产生足量对话历史（>1000 token），使 ContextUsage 百分比非零
	long := strings.Repeat("你好，这是一段用于触发上下文用量显示的较长消息。", 200)
	rt.AddToHistory("user", long)
	rt.AddToHistory("assistant", long)

	m.mode = modeNormal // 真实场景：发送后进入 normal 模式才开始流式
	m.streaming = true  // 流式处理中（压缩发生的时段）
	bar := renderStatusBar(m)

	if !strings.Contains(bar, "处理中") {
		t.Errorf("streaming 状态栏应显示处理中，got: %q", bar)
	}
	if !strings.Contains(bar, "ctx ") {
		t.Errorf("streaming 状态栏也应实时显示 ctx 用量（压缩后占比实时变化），got: %q", bar)
	}
	if strings.Contains(bar, "ctx 0%") {
		t.Errorf("ctx 用量应为非零百分比，got: %q", bar)
	}
}

func TestRenderStatusBar_ShowsCtxWhenIdle(t *testing.T) {
	rt := newStatusBarRuntime(t)
	rt.AddToHistory("user", "hello")
	m := initialModel(rt)
	m.streaming = false
	bar := renderStatusBar(m)
	if !strings.Contains(bar, "ctx ") {
		t.Errorf("空闲状态栏也应显示 ctx 用量，got: %q", bar)
	}
}

// TestRenderStatusBar_ModeLabel：状态栏模式标签必须跟随 rt.GetMode()，
// goal 模式切换后不得仍显示 chat（回归：只有 task 分支、漏掉 goal）。
func TestRenderStatusBar_ModeLabel(t *testing.T) {
	rt := newStatusBarRuntime(t)
	m := initialModel(rt)

	// 默认 chat
	if bar := renderStatusBar(m); !strings.Contains(bar, "chat") {
		t.Errorf("默认模式应显示 chat，got: %q", bar)
	}

	// task
	rt.SetMode("task")
	if bar := renderStatusBar(m); !strings.Contains(bar, "task") || strings.Contains(bar, "chat") {
		t.Errorf("task 模式状态栏应显示 task 而非 chat，got: %q", bar)
	}

	// goal（此前缺失的分支）
	rt.SetMode("goal")
	bar := renderStatusBar(m)
	if !strings.Contains(bar, "goal") {
		t.Errorf("goal 模式状态栏应显示 goal，got: %q", bar)
	}
	if strings.Contains(bar, "chat") {
		t.Errorf("goal 模式状态栏不得再显示 chat，got: %q", bar)
	}

	// 切回 chat 恢复
	rt.SetMode("chat")
	if bar := renderStatusBar(m); !strings.Contains(bar, "chat") || strings.Contains(bar, "goal") {
		t.Errorf("切回 chat 后状态栏应显示 chat，got: %q", bar)
	}
}

// TestTruncateANSIWidth：超宽行截断必须按终端显示宽度（CJK=2）计算、
// ANSI 样式码原样保留、截断落在内容中时补复位码，防止未闭合样式污染后续行。
func TestTruncateANSIWidth(t *testing.T) {
	// 1. 纯文本按 rune 截断
	if got := truncateANSIWidth("hello world", 5); got != "hello" {
		t.Errorf("纯文本截断: got %q", got)
	}
	// 2. maxW<=0 返回空
	if got := truncateANSIWidth("abc", 0); got != "" {
		t.Errorf("maxW=0 应返回空, got %q", got)
	}
	// 3. 短文本不截断
	if got := truncateANSIWidth("abc", 10); got != "abc" {
		t.Errorf("短文本不应截断, got %q", got)
	}
	// 4. ANSI 样式码不占宽度且原样保留
	styled := "\x1b[32mhello\x1b[0m"
	got := truncateANSIWidth(styled, 5)
	if !strings.Contains(got, "\x1b[32m") || !strings.Contains(got, "hello") {
		t.Errorf("ANSI 样式应保留且内容完整, got %q", got)
	}
	// 5. 截断点落在内容中时补 \x1b[0m 复位
	got = truncateANSIWidth("\x1b[32mhello world\x1b[0m", 5)
	if !strings.Contains(got, "\x1b[0m") {
		t.Errorf("截断后应补复位码防样式污染, got %q", got)
	}
	if strings.Contains(got, "world") {
		t.Errorf("应只保留前 5 个字符, got %q", got)
	}
	// 6. CJK 宽字符按 2 宽度计算（"你好ab"=2+2+1+1=6，截 5 应含"你好a"不含"b"）
	got = truncateANSIWidth("你好ab", 5)
	if !strings.Contains(got, "你好a") || strings.Contains(got, "b") {
		t.Errorf("CJK 应按 2 宽度截断, got %q", got)
	}
	// 7. 样式码在截断点之前，截断后样式+复位均保留
	got = truncateANSIWidth("前缀\x1b[1m加粗内容\x1b[0m尾部", 6)
	if !strings.Contains(got, "\x1b[1m") || !strings.Contains(got, "\x1b[0m") {
		t.Errorf("截断点前样式与复位码都应保留, got %q", got)
	}
}

// TestView_TotalLinesFitHeight：超宽行（system/error 消息）触发终端自动折行
// 会把实际显示行数撑大，顶出输入框/状态栏——View() 必须保证总行数恒 ≤ 终端高度，
// 且输入框（分隔线 ── + 光标 ▌）始终在底部可见。
func TestView_TotalLinesFitHeight(t *testing.T) {
	rt := newStatusBarRuntime(t)
	m := initialModel(rt)

	// 模拟运行中：消息区包含超长 system / error 消息（无宽度限制分支的重灾区）
	longSystem := strings.Repeat("系统提示超长内容", 30) // 180 字符，远超终端宽度
	longError := strings.Repeat("错误日志超长路径信息", 30)
	m.messages = append(m.messages, sysMsg(longSystem))
	m.messages = append(m.messages, ChatMessage{Type: MsgError, Content: longError})
	m.messages = append(m.messages, usrMsg("正常问题"))

	m.width = 100
	m.height = 30
	m.mode = modeNormal // 发送后 normal 模式：输入框应常驻

	out := m.View()
	lines := strings.Split(out, "\n")
	total := len(lines)
	if total > m.height {
		t.Errorf("View() 总行数 %d 超过终端高度 %d（超宽行折行会顶出输入框）", total, m.height)
	}
	// 输入框应可见：底部区域（输入区+状态栏）包含分隔线与光标
	tail := strings.Join(lines[len(lines)-6:], "\n")
	if !strings.Contains(tail, "─") || !strings.Contains(tail, "▌") {
		t.Errorf("输入框应常驻在底部可见（分隔线+光标），尾部: %q", tail)
	}
}

// TestView_HistoryLimitedToMaxLines：消息区历史渲染行数限制在 maxHistoryLines 内，
// 超长历史只保留最新行（最早内容不再显示/滚动可达），防止滚动查看卡顿。
func TestView_HistoryLimitedToMaxLines(t *testing.T) {
	rt := newStatusBarRuntime(t)
	m := initialModel(rt)
	m.width = 100
	m.height = 1200
	m.mode = modeNormal

	// 构造超长历史：600 条消息（每条渲染 ≥2 行 → 1200+ 行 > maxHistoryLines）
	m.messages = append(m.messages, usrMsg("HEAD_MARKER 最早的历史"))
	for i := 0; i < 598; i++ {
		m.messages = append(m.messages, usrMsg(fmt.Sprintf("历史消息 %d", i)))
	}
	m.messages = append(m.messages, usrMsg("TAIL_MARKER 最新的历史"))

	out := m.View()
	if strings.Contains(out, "HEAD_MARKER") {
		t.Errorf("超长历史应限制在 %d 行内：最早的 HEAD_MARKER 不应可见", maxHistoryLines)
	}
	if !strings.Contains(out, "TAIL_MARKER") {
		t.Errorf("最新内容 TAIL_MARKER 应保留可见")
	}
}

// TestView_HelpPanelFitsHeight：帮助面板打开时总行数也不得超过终端高度
// （输入框/状态栏恒在底部，修复"帮助残留导致输入框看不见"）。
func TestView_HelpPanelFitsHeight(t *testing.T) {
	rt := newStatusBarRuntime(t)
	m := initialModel(rt)
	m.width = 100
	m.height = 30
	m.showHelp = true
	m.mode = modeNormal

	out := m.View()
	lines := strings.Split(out, "\n")
	if len(lines) > m.height {
		t.Errorf("帮助面板打开时总行数 %d 超过终端高度 %d", len(lines), m.height)
	}
	tail := strings.Join(lines[len(lines)-6:], "\n")
	if !strings.Contains(tail, "─") || !strings.Contains(tail, "▌") {
		t.Errorf("帮助面板打开时输入框仍应在底部可见，尾部: %q", tail)
	}
}

// TestRenderCache_HitReusesLines 渲染缓存命中测试：签名不变时复用渲染行，
// 内容变化时自动失效重渲染（滚动卡顿修复的回归保护）。
func TestRenderCache_HitReusesLines(t *testing.T) {
	m := &model{renderCache: make(map[int]*msgRenderCache)}
	m.width = 100
	m.height = 30
	m.mode = modeNormal
	// 构造 3 条历史消息
	m.messages = []ChatMessage{
		asstMsg("历史消息一：上下文压缩方案实现完成"),
		sysMsg("工具执行成功"),
		asstMsg("历史消息二：goal 模式已支持"),
	}
	// 第一次渲染：全部 miss，写入缓存
	_ = m.View()
	if len(m.renderCache) != 3 {
		t.Fatalf("期望 3 条缓存，实际 %d", len(m.renderCache))
	}
	// 记录缓存行引用
	first := m.renderCache[0].lines
	// 第二次渲染（滚动/重绘）：签名不变，应全部命中（lines 引用相同）
	_ = m.View()
	if len(m.renderCache) != 3 {
		t.Fatalf("第二次渲染缓存数量变化: %d", len(m.renderCache))
	}
	if &m.renderCache[0].lines[0] != &first[0] {
		t.Fatal("缓存未命中：lines 被重新分配（应复用同一底层数组）")
	}
	// 第三条消息内容变化（流式追加）：仅该条失效重渲染
	m.messages[2].Content = "历史消息二：goal 模式已支持（更新）"
	_ = m.View()
	if len(m.renderCache) != 3 {
		t.Fatalf("更新后缓存数量变化: %d", len(m.renderCache))
	}
	if &m.renderCache[0].lines[0] != &first[0] {
		t.Fatal("未变化消息不应重新渲染")
	}
}

// TestRenderSignature_InvalidatesOnChange 签名变化测试：折叠/内容/流式任一变化签名都不同。
func TestRenderSignature_InvalidatesOnChange(t *testing.T) {
	base := thinkMsg()
	base.Content = "思考内容"
	if msgRenderSignature(base, 80) != msgRenderSignature(base, 80) {
		t.Fatal("相同消息签名应相同")
	}
	// 折叠变化
	c := base
	c.Collapsed = !c.Collapsed
	if msgRenderSignature(base, 80) == msgRenderSignature(c, 80) {
		t.Fatal("折叠变化应使签名不同")
	}
	// 内容变化
	c2 := base
	c2.Content += "x"
	if msgRenderSignature(base, 80) == msgRenderSignature(c2, 80) {
		t.Fatal("内容变化应使签名不同")
	}
	// 流式变化
	c3 := base
	c3.Streaming = !c3.Streaming
	if msgRenderSignature(base, 80) == msgRenderSignature(c3, 80) {
		t.Fatal("流式变化应使签名不同")
	}
}

// Bug: 命令建议面板渲染在 body 与子任务面板之间，但高度未计入固定区预算
// （旧实现 fixed 只含 panelH+inputH+statusLines）→ 总行数 = height + 面板行数，
// 输入框被顶出可视区（与"帮助面板/状态栏折行"同类布局超限 bug）。
func TestView_CommandSuggestionsFitHeight(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeCommand
	m.input = "/hi"
	// 6 条建议（renderSuggestions 最多显示 5 条 + "还有 N 条"提示行）
	m.completions = []CommandEntry{
		{Name: "history", Desc: "查看历史"}, {Name: "help", Desc: "帮助"},
		{Name: "ctx", Desc: "上下文"}, {Name: "skills", Desc: "技能"},
		{Name: "load", Desc: "加载"}, {Name: "session", Desc: "会话"},
	}
	m.width, m.height = 100, 30

	out := m.View()
	lines := strings.Split(out, "\n")
	total := len(lines)
	if total > m.height {
		t.Errorf("View() 总行数 %d 超过终端高度 %d（建议面板未计入预算，输入框会被顶出）", total, m.height)
	}
	// 建议面板内容应可见
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "history") || !strings.Contains(joined, "还有 1 条") {
		t.Errorf("命令建议面板内容缺失:\n%s", joined)
	}
	// 输入区应常驻底部可见（分隔线 + 光标）
	tail := strings.Join(lines[len(lines)-3:], "\n")
	if !strings.Contains(tail, "─") || !strings.Contains(tail, "▌") {
		t.Errorf("命令模式输入框应常驻底部可见，尾部: %q", tail)
	}

	// 输入含空格（命令已选定、正在输参数）→ 建议面板必须消失，不残留
	m.input = "/hi xxx"
	m.completions = []CommandEntry{{Name: "history", Desc: "查看历史"}}
	out2 := m.View()
	if strings.Contains(out2, "history") {
		t.Errorf("输入含空格后建议面板应消失（不残留），输出:\n%s", out2)
	}
}
