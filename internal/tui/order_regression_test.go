package tui

import (
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────
// 文字显示顺序错乱回归测试
//
// 背景：最近大改造（渲染缓存 msgRenderCache / maxHistoryLines 截断 /
// 流式分块 / 思考块折叠）后，用户报告 TUI 显示的文字顺序错乱。
// 本文件覆盖三类顺序场景：
//   1. 多轮对话后 View() 输出顺序 = 消息追加顺序
//   2. 流式增量（streamMsg 多 token + streamDoneMsg）不改变已渲染消息相对顺序
//   3. maxHistoryLines 截断后剩余消息顺序不变
//   4. 会话切换（/session /load /new）后 renderCache 不串会话
// ─────────────────────────────────────────────────────────────

// 从 View() 输出中按出现顺序提取 "▸ 你" 用户消息与 "● ContextLoom" 助手消息的序号，
// 返回用户消息文本顺序（出现顺序）。
func extractUserOrder(out string) []string {
	var order []string
	lines := strings.Split(out, "\n")
	for _, ln := range lines {
		if strings.Contains(ln, "▸ 你") {
			order = append(order, ln)
		}
	}
	return order
}

// 从 View() 输出中提取 "历史问题 N" 的 N 序列（验证截断后递增顺序）
// 注意：渲染输出中标题行（▸ 你 18:04）与内容行（历史问题 N）是两行，
// 因此按内容行匹配。
func extractHistorySeq(out string) []int {
	var seq []int
	for _, ln := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "历史问题 ") {
			rest := trimmed[len("历史问题 "):]
			var n int
			digits := ""
			for _, r := range rest {
				if r >= '0' && r <= '9' {
					digits += string(r)
				} else {
					break
				}
			}
			if digits != "" {
				for _, r := range digits {
					n = n*10 + int(r-'0')
				}
				seq = append(seq, n)
			}
		}
	}
	return seq
}

func TestView_MessageOrderMatchesAppendOrder(t *testing.T) {
	m := initialModel(nil)
	m.width = 120
	m.height = 40

	// 连续发送 5 条用户消息（模拟 send() 的追加逻辑）
	for i := 1; i <= 5; i++ {
		if len(m.messages) > 0 {
			m.appendMsg(ChatMessage{Type: MsgSystem, Content: "─ 时间线 ─"})
		}
		m.appendMsg(usrMsg("问题 " + itoa(i)))
		m.appendMsg(asstMsg("回答 " + itoa(i)))
	}

	out := m.View()
	got := extractUserOrder(out)
	if len(got) != 5 {
		t.Fatalf("期望 5 条用户消息，实际 %d 条\n输出:\n%s", len(got), out)
	}
	// 顺序必须严格递增：问题 1 在问题 2 之前 …
	// 注意：渲染中 "▸ 你" 标题行与 "问题 N" 正文行分离，按正文行定位。
	contentIdx := map[int]string{}
	for _, ln := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(ln)
		for i := 1; i <= 5; i++ {
			if strings.Contains(trimmed, "问题 "+itoa(i)) {
				contentIdx[i] = trimmed
			}
		}
	}
	for i := 1; i <= 5; i++ {
		if contentIdx[i] == "" {
			t.Fatalf("缺少 问题 %d 的正文行\n输出:\n%s", i, out)
		}
	}
	// 按输出行号验证顺序：问题 i 的行号必须 < 问题 i+1 的行号
	pos := map[int]int{}
	for i, ln := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(ln)
		for n := 1; n <= 5; n++ {
			if strings.Contains(trimmed, "问题 "+itoa(n)) {
				pos[n] = i
			}
		}
	}
	for n := 1; n < 5; n++ {
		if pos[n] >= pos[n+1] {
			t.Fatalf("顺序错乱：问题 %d(行%d) 应位于 问题 %d(行%d) 之前\n输出:\n%s", n, pos[n], n+1, pos[n+1], out)
		}
	}
	// 助手消息也按顺序出现（按正文行 "回答 N" 定位）
	aiPos := map[int]int{}
	for i, ln := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(ln)
		for n := 1; n <= 5; n++ {
			if strings.Contains(trimmed, "回答 "+itoa(n)) {
				aiPos[n] = i
			}
		}
	}
	for n := 1; n <= 5; n++ {
		if aiPos[n] == 0 {
			t.Fatalf("缺少 回答 %d 的正文行\n输出:\n%s", n, out)
		}
	}
	for n := 1; n < 5; n++ {
		if aiPos[n] >= aiPos[n+1] {
			t.Fatalf("助手消息顺序错乱：回答 %d(行%d) 应位于 回答 %d(行%d) 之前\n输出:\n%s", n, aiPos[n], n+1, aiPos[n+1], out)
		}
	}
	// 用户消息 i 必须位于助手消息 i 之前（同一轮内）
	for n := 1; n <= 5; n++ {
		if pos[n] >= aiPos[n] {
			t.Fatalf("同轮顺序错乱：问题 %d(行%d) 应位于 回答 %d(行%d) 之前\n输出:\n%s", n, pos[n], n, aiPos[n], out)
		}
	}
}

// 流式增量：模拟 send → 多 token 流式 → 工具消息 → done 的完整一轮，
// 断言 View() 输出中消息相对顺序不被流式处理打乱。
func TestView_StreamingIncrementalPreservesOrder(t *testing.T) {
	m := initialModel(nil)
	m.width = 120
	m.height = 40

	// 第 1 轮：完整一轮（用户 → 思考 → 正文 → 工具调用 → 工具结果）
	m.appendMsg(usrMsg("第一轮问题"))
	m.streaming = true
	m.streamBuf = ""
	// 模拟流式 token：思考部分 + 正文部分
	for _, tok := range []string{"<|reasoning|>", "分析中", "<|/reasoning|>", "第一轮回答"} {
		m.streamBuf += tok
	}
	// 工具消息到达：先把流式内容落为消息
	body, thinks := splitThinkTags(m.streamBuf)
	for _, t := range thinks {
		th := thinkMsg()
		th.Content = t
		th.Collapsed = true
		th.Streaming = false
		m.appendMsg(th)
	}
	if body != "" {
		m.appendMsg(asstMsg(body))
	}
	m.streamBuf = ""
	m.appendMsg(toolCall("read_file", `{"path":"a.go"}`))
	m.appendMsg(toolOK("read_file", "ok"))
	// 继续流式正文
	m.streamBuf = "第二轮回答开头"
	m.appendMsg(toolCall("exec_command", `{"cmd":"go test"}`))
	m.appendMsg(toolOK("exec_command", "PASS"))
	m.streamBuf = "第二轮回答结尾"
	// 结束
	body2, thinks2 := splitThinkTags(m.streamBuf)
	for _, t := range thinks2 {
		th := thinkMsg()
		th.Content = t
		th.Collapsed = true
		th.Streaming = false
		m.appendMsg(th)
	}
	if body2 != "" {
		m.appendMsg(asstMsg(body2))
	}
	m.streamBuf = ""
	m.streaming = false

	out := m.View()

	// 期望顺序：第一轮用户 → 第一轮思考 → 第一轮回答 → 工具调用 → 工具结果 → 第二轮回答开头+结尾
	// 注意：渲染中标题行（▸ 你/● ContextLoom）与内容行分离，因此用内容行定位。
	idx := map[string]int{}
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		switch {
		case strings.Contains(trimmed, "第一轮问题"):
			idx["u1"] = i
		case strings.Contains(trimmed, "💭 思考"):
			idx["think1"] = i
		case strings.Contains(trimmed, "第一轮回答"):
			idx["a1"] = i
		case strings.Contains(trimmed, "⚡ read_file"):
			idx["tool1"] = i
		case strings.Contains(trimmed, "✔ read_file"):
			idx["toolok1"] = i
		case strings.Contains(trimmed, "第二轮回答"):
			idx["a2"] = i
		}
	}
	order := []string{"u1", "think1", "a1", "tool1", "toolok1", "a2"}
	for i := 1; i < len(order); i++ {
		prev, cur := order[i-1], order[i]
		if _, ok := idx[prev]; !ok {
			t.Fatalf("缺少关键消息 %q 的输出行\n输出:\n%s", prev, out)
		}
		if _, ok := idx[cur]; !ok {
			t.Fatalf("缺少关键消息 %q 的输出行\n输出:\n%s", cur, out)
		}
		if idx[prev] >= idx[cur] {
			t.Fatalf("顺序错乱：%q(行%d) 应位于 %q(行%d) 之前\n输出:\n%s", prev, idx[prev], cur, idx[cur], out)
		}
	}
}

// maxHistoryLines 截断：构造超过 maxHistoryLines 行的历史，
// 断言剩余消息顺序不变（旧行被丢弃，新行保持相对顺序）。
func TestView_MaxHistoryLinesTruncatePreservesOrder(t *testing.T) {
	m := initialModel(nil)
	m.width = 60
	m.height = 40

	// 每条消息渲染 2 行（用户/助手各 2 行）→ 追加 600 条 = 1200+ 行 > 1000
	for i := 1; i <= 600; i++ {
		m.appendMsg(usrMsg("历史问题 " + itoa(i)))
		m.appendMsg(asstMsg("历史回答 " + itoa(i)))
	}

	out := m.View()
	// 最新的消息必须还在（尾部）
	if !strings.Contains(out, "历史问题 600") || !strings.Contains(out, "历史回答 600") {
		t.Fatalf("最新消息丢失\n输出尾部:\n%s", tailLines(out, 20))
	}
	// 顺序必须保持：检查输出中消息的序号严格递增
	seq := extractHistorySeq(out)
	if len(seq) < 2 {
		t.Fatalf("截断后消息过少: %d", len(seq))
	}
	for i := 1; i < len(seq); i++ {
		if seq[i] <= seq[i-1] {
			t.Fatalf("截断后消息顺序错乱: %v", seq)
		}
	}
}

// 会话切换：/session /load /new 切换后 renderCache 必须清空，
// 否则新会话 msgIdx 命中旧会话缓存 → 显示旧会话内容（顺序/内容错乱）。
func TestSessionSwitch_ClearsRenderCache(t *testing.T) {
	m := initialModel(nil)
	m.width = 120
	m.height = 40

	// 会话 A：两条消息，先渲染（填充 renderCache）
	m.appendMsg(usrMsg("会话A的问题"))
	m.appendMsg(asstMsg("会话A的回答"))
	outA := m.View()
	if !strings.Contains(outA, "会话A的问题") {
		t.Fatalf("会话A渲染异常")
	}
	if len(m.renderCache) == 0 {
		t.Fatalf("前置条件失败：renderCache 应为空（测试框架未走 View 缓存路径）或已填充")
	}

	// 切换会话：resetMessages 清空消息 + 渲染缓存 + clickMap + 滚动（模拟 commands.go 会话切换路径）
	m.resetMessages()
	if len(m.messages) != 0 {
		t.Fatalf("会话切换后应无消息")
	}
	// 关键断言：renderCache 必须清空，否则旧缓存残留
	if len(m.renderCache) != 0 {
		t.Fatalf("会话切换后 renderCache 未清空（残留 %d 条）→ 新会话 msgIdx 将命中旧缓存导致显示错乱", len(m.renderCache))
	}
}

// 真实场景回归：会话 A 渲染后切换会话 B，B 的消息若命中 A 的渲染缓存
// （相同 msgIdx + 相同签名），会显示 A 的内容 → 内容/顺序错乱。
// 修复前（m.messages=nil 不清 renderCache）：B 的 msgIdx 0 与 A 的 msgIdx 0
// 签名相同（同类型同内容模板）时显示 A 的旧渲染行。
func TestSessionSwitch_NoCrossSessionCacheHit(t *testing.T) {
	m := initialModel(nil)
	m.width = 120
	m.height = 40

	// 会话 A：msgIdx 0/1 是 "AAA" 模板
	m.appendMsg(usrMsg("AAA 会话A问题"))
	m.appendMsg(asstMsg("AAA 会话A回答"))
	outA := m.View()
	if !strings.Contains(outA, "AAA 会话A问题") {
		t.Fatalf("会话A渲染异常")
	}

	// 切换会话 B：新消息内容与 A 完全不同（但类型/结构相同）
	m.resetMessages()
	m.appendMsg(usrMsg("BBB 会话B问题"))
	m.appendMsg(asstMsg("BBB 会话B回答"))
	outB := m.View()

	if !strings.Contains(outB, "BBB 会话B问题") {
		t.Fatalf("会话B渲染丢失自己的内容（可能命中了会话A缓存）\n输出:\n%s", outB)
	}
	if strings.Contains(outB, "AAA 会话A问题") {
		t.Fatalf("会话B渲染串入了会话A内容（renderCache 跨会话命中）\n输出:\n%s", outB)
	}
}

// 流式中途工具消息打断思考块：模拟 "思考中 → 工具调用 → 思考继续 → 正文" 的
// 真实交错流式序列，断言最终消息列表与 View() 输出顺序与文本出现顺序一致。
// 这是"运行中文字顺序错乱"的高发场景（思考块 + 工具消息 + 正文交错）。
func TestView_StreamInterleavedToolPreservesOrder(t *testing.T) {
	m := initialModel(nil)
	m.width = 120
	m.height = 40

	m.appendMsg(usrMsg("交错问题"))
	m.streaming = true
	m.streamBuf = ""

	// 阶段1：思考前半
	m.streamBuf = "<|reasoning|>先分析<|/reasoning|>正文第一部分"
	// 工具消息到达：streamBuf 落为消息（thinks→body），再追加工具
	body1, thinks1 := splitThinkTags(m.streamBuf)
	for _, tk := range thinks1 {
		th := thinkMsg()
		th.Content = tk
		th.Collapsed = true
		th.Streaming = false
		m.appendMsg(th)
	}
	if body1 != "" {
		m.appendMsg(asstMsg(body1))
	}
	m.streamBuf = ""
	m.appendMsg(toolCall("exec_command", `{"cmd":"pwd"}`))
	m.appendMsg(toolOK("exec_command", "/tmp"))

	// 阶段2：第二轮思考 + 正文结尾
	m.streamBuf = "<|reasoning|>继续验证<|/reasoning|>正文第二部分"
	body2, thinks2 := splitThinkTags(m.streamBuf)
	for _, tk := range thinks2 {
		th := thinkMsg()
		th.Content = tk
		th.Collapsed = true
		th.Streaming = false
		m.appendMsg(th)
	}
	if body2 != "" {
		m.appendMsg(asstMsg(body2))
	}
	m.streamBuf = ""
	m.streaming = false

	out := m.View()

	// 期望顺序（内容行定位）：
	// u1(交错问题) → think1(先分析) → a1(正文第一部分) → tool1(exec_command)
	// → toolok1(✔ exec_command) → think2(继续验证) → a2(正文第二部分)
	idx := map[string]int{}
	thinkIdx := 0
	for i, ln := range strings.Split(out, "\n") {
		tr := strings.TrimSpace(ln)
		switch {
		case strings.Contains(tr, "交错问题"):
			idx["u1"] = i
		case strings.Contains(tr, "💭 思考完成"):
			// 折叠态思考块标题行（内容不可见），按出现顺序编号
			thinkIdx++
			idx["think"+itoa(thinkIdx)] = i
		case strings.Contains(tr, "正文第一部分"):
			idx["a1"] = i
		case strings.Contains(tr, "⚡ exec_command"):
			idx["tool1"] = i
		case strings.Contains(tr, "✔ exec_command"):
			idx["toolok1"] = i
		case strings.Contains(tr, "正文第二部分"):
			idx["a2"] = i
		}
	}
	order := []string{"u1", "think1", "a1", "tool1", "toolok1", "think2", "a2"}
	for i := 1; i < len(order); i++ {
		prev, cur := order[i-1], order[i]
		if _, ok := idx[prev]; !ok {
			t.Fatalf("缺少 %q 输出行\n输出:\n%s", prev, out)
		}
		if _, ok := idx[cur]; !ok {
			t.Fatalf("缺少 %q 输出行\n输出:\n%s", cur, out)
		}
		if idx[prev] >= idx[cur] {
			t.Fatalf("顺序错乱：%q(行%d) 应位于 %q(行%d) 之前\n输出:\n%s", prev, idx[prev], cur, idx[cur], out)
		}
	}
}
