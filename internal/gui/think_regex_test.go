package gui

// 前端思考标签/滚动逻辑回归测试。
//
// 背景：需求"思考标签默认折叠、点击展开、可查看实时思考"的前端核心是
// app.js 的 parseAssistantBody + fillAssistantBubble：
//   - parseAssistantBody 用 THINK_RE（闭合分支带 |$）解析——思考标签【未闭合】
//     （流式中间态）也生成思考块（closed=false，内容=当前剩余全部，随流式
//     逐帧增长），真正闭合后 closed=true——等效"先补闭合再删重复"；
//   - fillAssistantBubble 流式逐帧【结构同构时增量更新】（保留 DOM 节点与
//     点击状态，不闪烁不破坏焦点），结构变化才重建（恢复已展开块）。
// 本测试直接从 app.js 提取生产正则（与运行时代码 100% 一致），用 Go regexp
// 验证其行为契约，杜绝"改前端忘回归"。
//
// 同时验证需求 2 的滚动契约：离开底部（哪怕 1 行）即暂停自动跟随，
// 仅"回到底部"按钮/主动回底恢复跟随（阈值必须 ≤ 8px）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func loadAppJS(t *testing.T) string {
	t.Helper()
	p := filepath.Join("..", "..", "cmd", "contextloom-gui", "web", "app.js")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 app.js: %v", err)
	}
	return string(b)
}

// extractRegExpFrom 从 "const NAME = new RegExp(\"<pattern>\", \"g\")" 提取
// pattern 并转成 Go 正则（JS 字符串转义 \\ → \）。
func extractRegExpFrom(t *testing.T, src, decl string) *regexp.Regexp {
	t.Helper()
	idx := strings.Index(src, decl)
	if idx < 0 {
		t.Fatalf("app.js 中找不到声明 %s", decl)
	}
	rest := src[idx+len(decl):]
	end := strings.Index(rest, `", "g")`)
	if end < 0 {
		end = strings.Index(rest, `")`)
	}
	if end < 0 {
		t.Fatalf("%s 正则字符串未闭合", decl)
	}
	pat := strings.ReplaceAll(rest[:end], `\\`, `\`)
	re, err := regexp.Compile(pat)
	if err != nil {
		t.Fatalf("%s 编译失败: %v (pattern=%q)", decl, err, pat)
	}
	return re
}

// thinkPart 复刻 parseAssistantBody 输出的段落结构。
type thinkPart struct {
	Type    string // "think" / "text"
	Content string
	Closed  bool // 仅 think 有意义：true=闭合标签已到达
}

// parseThinkParts 复刻 parseAssistantBody 的分块纯逻辑（不依赖 DOM）：
//   - 未闭合思考标签也生成 think 块（内容=当前剩余全部，closed=false）
//   - 尾部半截标签（<th/<thi/<| 等未完整到达）剔除
func parseThinkParts(body string, re *regexp.Regexp) []thinkPart {
	var parts []thinkPart
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(body, -1) {
		seg := strings.TrimSpace(body[last:m[0]])
		if seg != "" {
			parts = append(parts, thinkPart{Type: "text", Content: seg})
		}
		matched := body[m[0]:m[1]]
		closed := strings.HasSuffix(matched, "</think>") ||
			strings.HasSuffix(matched, "</thinking>") ||
			strings.HasSuffix(matched, "<|/reasoning|>")
		parts = append(parts, thinkPart{Type: "think", Content: body[m[2]:m[3]], Closed: closed})
		last = m[1]
	}
	tail := strings.TrimSpace(body[last:])
	// 半截标签剔除：最后一个 "<" 到末尾的片段是思考标签前缀则剔除
	if li := strings.LastIndex(tail, "<"); li >= 0 {
		frag := tail[li:]
		targets := []string{"think", "thinking", "|reasoning|"}
		half := frag == "<"
		if !half {
			for _, tgt := range targets {
				if len(frag) <= len(tgt)+1 && strings.HasPrefix(tgt, frag[1:]) {
					half = true
					break
				}
			}
		}
		if half {
			tail = strings.TrimSpace(tail[:li])
		}
	}
	if tail != "" {
		parts = append(parts, thinkPart{Type: "text", Content: tail})
	}
	return parts
}

// 无内容丢失：正文段 + 思考块内容 + 尾部拼接后必须还原原始输入
// （fillAssistantBubble 中块间正文段也会渲染，任何字符都不能吞）。
func assertNoContentLoss(t *testing.T, body string, re *regexp.Regexp, parts []thinkPart) {
	t.Helper()
	// 期望：去掉全部思考标签（保留块内容）后的原文
	expected := re.ReplaceAllString(body, "$1")
	// 实际渲染拼接：各段顺序拼接
	var sb strings.Builder
	for _, p := range parts {
		sb.WriteString(p.Content)
	}
	if sb.String() != expected {
		t.Errorf("内容丢失/错位:\n  期望=%q\n  实际=%q", expected, sb.String())
	}
}

// 需求 1 核心契约：思考标签一出现（未闭合）立即生成思考块，内容随流式
// 逐帧增长；闭合标签到达后自然闭合（closed=true）；正文随后正常输出。
func TestFrontendThink_UnclosedRealTime(t *testing.T) {
	src := loadAppJS(t)
	re := extractRegExpFrom(t, src, `const THINK_RE = new RegExp("`)

	// 帧1：<think> 刚出现 → 立即生成未闭合思考块（不再"只显示光标"）
	parts := parseThinkParts("<think>", re)
	if len(parts) != 1 || parts[0].Type != "think" || parts[0].Closed {
		t.Fatalf("帧1 应生成未闭合思考块: %+v", parts)
	}
	// 帧2：思考内容增长 → 块内容实时更新
	parts = parseThinkParts("<think>正在思考", re)
	if len(parts) != 1 || parts[0].Content != "正在思考" || parts[0].Closed {
		t.Fatalf("帧2 思考块内容应实时增长: %+v", parts)
	}
	// 帧3：闭合标签到达 → 自然闭合（等效"补闭合后去重"）
	parts = parseThinkParts("<think>正在思考</think>", re)
	if len(parts) != 1 || parts[0].Content != "正在思考" || !parts[0].Closed {
		t.Fatalf("帧3 应自然闭合: %+v", parts)
	}
	// 帧4：正文输出
	parts = parseThinkParts("<think>正在思考</think>正文来了", re)
	if len(parts) != 2 || parts[0].Type != "think" || !parts[0].Closed ||
		parts[1].Type != "text" || parts[1].Content != "正文来了" {
		t.Fatalf("帧4 应输出正文: %+v", parts)
	}
}

func TestFrontendThinkRegex_ThreeFormats(t *testing.T) {
	src := loadAppJS(t)
	re := extractRegExpFrom(t, src, `const THINK_RE = new RegExp("`)

	body := "正文A<think>思考1</think>正文B<thinking>思考2</thinking>正文C<|reasoning|>思考3<|/reasoning|>正文D"
	parts := parseThinkParts(body, re)
	var thinks []string
	for _, p := range parts {
		if p.Type == "think" {
			thinks = append(thinks, p.Content)
			if !p.Closed {
				t.Errorf("块 %q 应已闭合", p.Content)
			}
		}
	}
	if len(thinks) != 3 || thinks[0] != "思考1" || thinks[1] != "思考2" || thinks[2] != "思考3" {
		t.Fatalf("应匹配 3 个思考块, got %v", thinks)
	}
	if parts[len(parts)-1].Type != "text" || parts[len(parts)-1].Content != "正文D" {
		t.Errorf("尾部正文 = %+v, want 正文D", parts[len(parts)-1])
	}
	assertNoContentLoss(t, body, re, parts)
}

func TestFrontendThinkRegex_MultiBlockMiddleBody(t *testing.T) {
	src := loadAppJS(t)
	re := extractRegExpFrom(t, src, `const THINK_RE = new RegExp("`)

	body := "P1<think>t1</think>P2<thinking>t2</thinking>P3<|reasoning|>t3<|/reasoning|>P4"
	parts := parseThinkParts(body, re)
	// 中间正文 P2/P3 不能丢（parseAssistantBody 中块间正文段也会渲染）
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Content)
		}
	}
	if strings.Join(texts, "|") != "P1|P2|P3|P4" {
		t.Errorf("中间正文丢失: %v", texts)
	}
	assertNoContentLoss(t, body, re, parts)
}

// 语义反转契约：未闭合思考块【不再截断剔除】，而是生成思考块（closed=false，
// 内容=剩余全部）——思考中可实时折叠查看（需求 1）。
func TestFrontendThink_UnclosedKeptAsBlock(t *testing.T) {
	src := loadAppJS(t)
	re := extractRegExpFrom(t, src, `const THINK_RE = new RegExp("`)

	cases := []struct {
		in       string
		wantLast thinkPart
	}{
		{"正文X<think>思考中...", thinkPart{Type: "think", Content: "思考中...", Closed: false}},
		{"正文Y<|reasoning|>还在想", thinkPart{Type: "think", Content: "还在想", Closed: false}},
		{"正文Z<thinking>未闭合", thinkPart{Type: "think", Content: "未闭合", Closed: false}},
	}
	for _, c := range cases {
		parts := parseThinkParts(c.in, re)
		last := parts[len(parts)-1]
		if last.Type != c.wantLast.Type || last.Content != c.wantLast.Content || last.Closed != c.wantLast.Closed {
			t.Errorf("输入 %q: 末段 = %+v, want %+v", c.in, last, c.wantLast)
		}
	}
}

// 半截标签（流式分块到达的中间态）剔除，正常文本不误删。
func TestFrontendThink_HalfTagStripped(t *testing.T) {
	src := loadAppJS(t)
	re := extractRegExpFrom(t, src, `const THINK_RE = new RegExp("`)

	cases := []struct{ in, wantTail string }{
		{"<think>abc</think>tail<thi", "tail"},
		{"<think>abc</think>tail<th", "tail"},
		{"<think>abc</think>tail<|", "tail"},
		{"<think>abc</think>tail<|reasoning", "tail"},
		{"<think>abc</think>tail<the doc", "tail<the doc"}, // 非前缀不误删
		{"see <the doc", "see <the doc"},                   // 纯正文不受影响
	}
	for _, c := range cases {
		parts := parseThinkParts(c.in, re)
		last := parts[len(parts)-1]
		if last.Type != "text" || last.Content != c.wantTail {
			t.Errorf("输入 %q: 尾部 = %+v, want text(%q)", c.in, last, c.wantTail)
		}
	}
}

// 跨格式闭合（畸形模型输出）：不得吞掉中间正文，每段内容都必须被渲染。
func TestFrontendThinkRegex_CrossFormatNoSwallow(t *testing.T) {
	src := loadAppJS(t)
	re := extractRegExpFrom(t, src, `const THINK_RE = new RegExp("`)

	// <think> 开 + </thinking> 闭（格式混用，真实模型偶发）
	body := "A<think>AAA</thinking>BBB<think>CCC</think>D"
	parts := parseThinkParts(body, re)
	var thinks []string
	for _, p := range parts {
		if p.Type == "think" {
			thinks = append(thinks, p.Content)
		}
	}
	if len(thinks) != 2 {
		t.Fatalf("应匹配 2 块, got %v", thinks)
	}
	assertNoContentLoss(t, body, re, parts) // BBB 必须保留

	// 未闭合嵌套：内容整体保留（不吞正文）
	body2 := "<think>AAA<|reasoning|>BBB<|/reasoning|>CCC</think>"
	parts2 := parseThinkParts(body2, re)
	assertNoContentLoss(t, body2, re, parts2)
}

func TestFrontendThinkRegex_NoThinkPlainText(t *testing.T) {
	src := loadAppJS(t)
	re := extractRegExpFrom(t, src, `const THINK_RE = new RegExp("`)

	body := "普通内容，没有思考标签"
	parts := parseThinkParts(body, re)
	if len(parts) != 1 || parts[0].Type != "text" || parts[0].Content != body {
		t.Errorf("无思考块时应原样保留: %+v", parts)
	}
}

// 需求 2 滚动契约：离开底部（≥1 行）立即暂停自动跟随；回到底部按钮存在且恢复跟随。
func TestFrontendScroll_StrictFollowContract(t *testing.T) {
	src := loadAppJS(t)

	// ① 阈值必须小（≤8px）：用户上翻即暂停，不得"离开底部仍强制下滑"
	awayRe := regexp.MustCompile(`scrollHeight - list\.scrollTop - list\.clientHeight > (\d+)`)
	m := awayRe.FindStringSubmatch(src)
	if m == nil {
		t.Fatal("app.js 找不到滚动监听器的 away 判定")
	}
	n, _ := strconv.Atoi(m[1])
	if n > 8 {
		t.Errorf("滚动暂停阈值 %dpx 过大：用户上翻 %dpx 内仍被强制拉回底部（违反需求 2）", n, n)
	}

	nearRe := regexp.MustCompile(`scrollHeight - list\.scrollTop - list\.clientHeight < (\d+)`)
	m2 := nearRe.FindStringSubmatch(src)
	if m2 == nil {
		t.Fatal("app.js 找不到 scrollBottom 的 nearBottom 判定")
	}
	n2, _ := strconv.Atoi(m2[1])
	if n2 > 8 {
		t.Errorf("scrollBottom 底部容差 %dpx 过大", n2)
	}

	// ② 回到底部按钮：存在、点击后恢复自动跟随（userScrolledUp=false）
	if !strings.Contains(src, `scrollDownBtn.onclick`) {
		t.Fatal("缺少回到底部按钮处理（scrollDownBtn.onclick）")
	}
	if !strings.Contains(src, "state.userScrolledUp = false") {
		t.Fatal("回到底部按钮未恢复自动跟随（userScrolledUp=false）")
	}
	if !strings.Contains(src, "l.scrollTop = l.scrollHeight") {
		t.Fatal("回到底部按钮未滚动到底部")
	}
}

// 需求 1 交互契约：思考块点击展开/默认折叠的 CSS 类必须存在；
// 思考中（未闭合）呼吸指示样式；增量更新不重建 DOM 的 done 标记。
func TestFrontendThink_CSSContract(t *testing.T) {
	// style.css 与 app.js 的类名契约（.think-body 默认隐藏 / .open 展开）
	cssPath := filepath.Join("..", "..", "cmd", "contextloom-gui", "web", "style.css")
	b, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("读取 style.css: %v", err)
	}
	css := string(b)
	for _, must := range []string{
		".think-body {",                 // 思考体样式块
		"display: none;",                // 默认折叠
		".think-block.open .think-body", // 点击展开
		"display: block;",               // 展开显示
		".think-head",                   // 可点击头部
		".think-block:not(.done)",       // 思考中（未闭合）呼吸指示
		"thinkBlink",                    // 呼吸动画
	} {
		if !strings.Contains(css, must) {
			t.Errorf("style.css 缺少契约 %q", must)
		}
	}
	src := loadAppJS(t)
	for _, must := range []string{
		`tb.querySelector(".think-head").onclick`, // 点击切换展开
		`classList.toggle("open")`,
		`classList.toggle("done", p.closed)`, // 闭合标记（增量更新）
		`parseAssistantBody`,                 // 未闭合也生成思考块
		`bubble.innerHTML = ""`,              // 结构变化才重建
	} {
		if !strings.Contains(src, must) {
			t.Errorf("app.js 缺少思考块交互契约 %q", must)
		}
	}
}
