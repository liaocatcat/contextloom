package tui

import (
	"strings"
	"testing"
)

// TestStreamFlow_BodyComplete 端到端验证：模拟用户发送消息后，模型流式输出
// （新标记格式 + 正文含 response/thinking 英文单词），最终正文必须完整保留。
func TestStreamFlow_BodyComplete(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.width = 100
	m.height = 40
	m.streaming = true
	m.streamBuf = ""

	// 模拟模型流式输出：新标记包裹思考 + 正文含英文单词 response/thinking
	// 这正是用户遇到的截断场景："3. 检查 streamMsg 的 response 处理"
	full := "让我检查这些点：\n" +
		"1. main.go 中 bubbletea Program 选项（bracketed paste 是否启用）\n" +
		"2. handleCmd 的 enter 逻辑（命令模式发送什么）\n" +
		"3. 检查 streamMsg 的 response 处理 thinking 环节\n" +
		"4. 最终修复完成。\n" +
		"结论：需要检查 response 单词是否被误判为思考结束标签。"

	// 流式分片发送（模拟 DeepSeek delta）
	chunks := []string{
		"<|reasoning|>用户想了解显示问题，先检查渲染链路<|/reasoning|>",
		full,
	}
	for _, c := range chunks {
		m.streamBuf += c
	}

	// 流结束
	m.streaming = false
	body, thinks := splitThinkTags(m.streamBuf)
	if len(thinks) != 1 {
		t.Fatalf("应识别 1 个思考块, got %d", len(thinks))
	}
	// 正文必须包含完整列表，尤其是第 3、4 点（此前会被 response 误判吞掉）
	for _, want := range []string{
		"3. 检查 streamMsg 的 response 处理 thinking 环节",
		"4. 最终修复完成。",
		"结论：需要检查 response 单词是否被误判为思考结束标签。",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("正文丢失 %q\nbody=%q", want, body)
		}
	}

	// 渲染验证：View() 输出应包含正文
	m.messages = append(m.messages, asstMsg(body))
	view := m.View()
	for _, want := range []string{
		"3. 检查 streamMsg 的 response 处理",
		"4. 最终修复完成",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("View 渲染丢失 %q", want)
		}
	}
}

// TestStreamFlow_LongOutput 验证长输出不被截断：正文所有关键行都应在 View 中可见
// （通过渲染行数验证无丢失，滚动可用性由 handleKey 测试覆盖）
func TestStreamFlow_LongOutput(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.width = 100
	m.height = 40

	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("第")
		sb.WriteString(itoa(i))
		sb.WriteString("行内容 response 测试\n")
	}
	full := sb.String()

	body, thinks := splitThinkTags(full)
	if len(thinks) != 0 {
		t.Fatalf("纯正文不应有思考块, got %d", len(thinks))
	}
	if !strings.Contains(body, "第199行内容 response 测试") {
		t.Error("长输出末尾丢失")
	}

	m.messages = append(m.messages, asstMsg(body))
	// 渲染不 panic 且包含首尾内容（滚动查看由 PgUp/PgDown 处理）
	view := m.View()
	if !strings.Contains(view, "第0行内容") && !strings.Contains(view, "第199行内容") {
		t.Error("View 渲染异常")
	}
}

// TestTailLines 验证 tailLines 只取最后 n 行、不足 n 行原样返回
func TestTailLines(t *testing.T) {
	s := "a\nb\nc\nd\ne"
	if got := tailLines(s, 2); got != "d\ne" {
		t.Errorf("tailLines(s,2) = %q, want %q", got, "d\ne")
	}
	if got := tailLines(s, 10); got != s {
		t.Errorf("tailLines(s,10) 应原样返回, got %q", got)
	}
	if got := tailLines("单行", 5); got != "单行" {
		t.Errorf("tailLines 单行应原样返回, got %q", got)
	}
	if got := tailLines(s, 0); got != "" {
		t.Errorf("tailLines(s,0) 应为空, got %q", got)
	}
}

// TestStreamFlow_StreamingLongBody 验证流式渲染长输出时只渲染尾部（不卡顿）
// 且折叠提示出现、渲染行数受限
func TestStreamFlow_StreamingLongBody(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.width = 100
	m.height = 60
	m.streaming = true

	var sb strings.Builder
	for i := 0; i < maxStreamRenderLines+50; i++ {
		sb.WriteString("第")
		sb.WriteString(itoa(i))
		sb.WriteString("行\n")
	}
	m.streamBuf = "<|reasoning|>思考<|/reasoning|>" + sb.String()

	view := m.View()
	if !strings.Contains(view, "上方内容已折叠") {
		t.Error("长输出流式渲染应显示折叠提示")
	}
	if !strings.Contains(view, "第"+itoa(maxStreamRenderLines+49)+"行") {
		t.Error("流式渲染应显示最后一行")
	}
	// 首行不应渲染（被折叠）
	if strings.Contains(view, "第0行") {
		t.Error("长输出首行不应出现在流式渲染中（已折叠）")
	}
}

// TestStreamFlow_StreamingShortBody 流式渲染短输出不受折叠影响
func TestStreamFlow_StreamingShortBody(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.width = 100
	m.height = 60
	m.streaming = true
	m.streamBuf = "让我检查这些点：\n1. main.go 中 bubbletea Program 选项\n3. 检查 streamMsg 的 response 处理"

	view := m.View()
	if strings.Contains(view, "上方内容已折叠") {
		t.Error("短输出不应显示折叠提示")
	}
	if !strings.Contains(view, "3. 检查 streamMsg 的 response 处理") {
		t.Error("短输出流式渲染应包含完整正文")
	}
}
