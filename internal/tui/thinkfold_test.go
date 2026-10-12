package tui

import (
	"strings"
	"testing"
)

func TestThinkFold_FullFlow(t *testing.T) {
	// 模拟 StreamEx 输出的完整内容（思考+正文）
	streamBuf := "\x3cthink\x3e用户想知道文件内容，需要先读取\x3c/think\x3e我来读取这个文件。"

	body, thinks := splitThinkTags(streamBuf)

	if body != "我来读取这个文件。" {
		t.Errorf("body expected '我来读取这个文件。', got %q", body)
	}
	if len(thinks) != 1 {
		t.Fatalf("expected 1 think, got %d", len(thinks))
	}
	if thinks[0] != "用户想知道文件内容，需要先读取" {
		t.Errorf("think content mismatch: %q", thinks[0])
	}

	// 验证 thinkMsg 创建
	th := thinkMsg()
	th.Content = thinks[0]
	th.Collapsed = true
	th.Streaming = false

	if th.Type != MsgThinking {
		t.Errorf("type expected MsgThinking, got %v", th.Type)
	}
	if !th.Collapsed {
		t.Error("expected Collapsed=true")
	}
	if th.Streaming {
		t.Error("expected Streaming=false")
	}

	// 验证渲染（折叠状态）
	rendered := renderMessage(th, 80, false)
	if !strings.Contains(rendered, "思考完成") {
		t.Errorf("rendered should contain '思考完成', got: %s", rendered)
	}
}

func TestThinkFold_OnlyThinking(t *testing.T) {
	// 只有思考，无正文（模型调用工具时）
	streamBuf := "\x3cthink\x3e用户想了解文件内容\x3c/think\x3e"

	body, thinks := splitThinkTags(streamBuf)

	if body != "" {
		t.Errorf("body should be empty, got %q", body)
	}
	if len(thinks) != 1 {
		t.Fatalf("expected 1 think, got %d", len(thinks))
	}
}

func TestThinkFold_NoTags(t *testing.T) {
	// 没有思考标签（简单问题）
	streamBuf := "你好！我是ContextLoom。"

	body, thinks := splitThinkTags(streamBuf)

	if body != "你好！我是ContextLoom。" {
		t.Errorf("body mismatch: %q", body)
	}
	if len(thinks) != 0 {
		t.Errorf("expected 0 thinks, got %d", len(thinks))
	}
}

func TestThinkFold_MultipleToolRounds(t *testing.T) {
	// 第一轮：思考+工具调用
	round1 := "\x3cthink\x3e需要读取文件\x3c/think\x3e"
	body1, thinks1 := splitThinkTags(round1)
	if body1 != "" || len(thinks1) != 1 {
		t.Errorf("round1: body=%q thinks=%d", body1, len(thinks1))
	}

	// 第二轮：思考+正文
	round2 := "\x3cthink\x3e文件内容已获取\x3c/think\x3e文件实现了5个功能。"
	body2, thinks2 := splitThinkTags(round2)
	if body2 != "文件实现了5个功能。" || len(thinks2) != 1 {
		t.Errorf("round2: body=%q thinks=%d", body2, len(thinks2))
	}
}

func TestThinkFold_ThinkingTag(t *testing.T) {
	// <thinking> 完整标签（带 ing）
	streamBuf := "<thinking>需要先分析需求</thinking>好的，我来处理。"

	body, thinks := splitThinkTags(streamBuf)
	if body != "好的，我来处理。" {
		t.Errorf("body expected '好的，我来处理。', got %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "需要先分析需求" {
		t.Errorf("thinks mismatch: %v", thinks)
	}
}

func TestThinkFold_UnclosedThinking(t *testing.T) {
	// <thinking> 未闭合：思考内容不泄漏到正文
	streamBuf := "<thinking>正在思考中"

	body, thinks := splitThinkTags(streamBuf)
	if body != "" {
		t.Errorf("未闭合 thinking 不应输出正文, got %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "正在思考中" {
		t.Errorf("thinks mismatch: %v", thinks)
	}
}

func TestThinkFold_MixedTags(t *testing.T) {
	// 混合使用 <think> 和 <thinking>
	streamBuf := "<thinking>第一个思考</thinking>正文一<think>第二个思考</think>正文二"

	body, thinks := splitThinkTags(streamBuf)
	if body != "正文一正文二" {
		t.Errorf("body expected '正文一正文二', got %q", body)
	}
	if len(thinks) != 2 {
		t.Fatalf("expected 2 thinks, got %d: %v", len(thinks), thinks)
	}
	if thinks[0] != "第一个思考" || thinks[1] != "第二个思考" {
		t.Errorf("thinks mismatch: %v", thinks)
	}
}

func TestThinkFold_PathNotMatched(t *testing.T) {
	// 路径中的 think/thinking 子串不应被误判为思考标签（需求4）
	streamBuf := "文件路径是 /home/user/thinking_project/think_notes.txt 请读取"

	body, thinks := splitThinkTags(streamBuf)
	if body != streamBuf {
		t.Errorf("路径内容不应被过滤, body=%q", body)
	}
	if len(thinks) != 0 {
		t.Errorf("路径不应被识别为思考块, thinks=%v", thinks)
	}
}

func TestThinkFold_LegacyLineStart(t *testing.T) {
	// 回归：行首 thinking...response 旧格式应被识别为思考块（修复 "thinking用户" 泄漏）
	streamBuf := "thinking\n用户的问题需要分析\nresponse\n好的，我来处理。"

	body, thinks := splitThinkTags(streamBuf)
	if body != "\n好的，我来处理。" {
		t.Errorf("body expected '\\n好的，我来处理。', got %q", body)
	}
	if len(thinks) != 1 {
		t.Fatalf("应识别到 1 个思考块, got %d: %v", len(thinks), thinks)
	}
	// 同一行空格前缀格式
	streamBuf2 := "正文 thinking思考内容 response结尾"
	body2, thinks2 := splitThinkTags(streamBuf2)
	if body2 != "正文结尾" {
		t.Errorf("body2 expected '正文结尾', got %q", body2)
	}
	if len(thinks2) != 1 {
		t.Errorf("应识别到 1 个思考块, got %d", len(thinks2))
	}
	// 未闭合：当作普通正文保留（与 StripThinking 一致），不吞正文
	streamBuf3 := "thinking 正在分析中"
	body3, thinks3 := splitThinkTags(streamBuf3)
	if body3 != streamBuf3 {
		t.Errorf("未闭合旧格式应保留为正文, got %q", body3)
	}
	if len(thinks3) != 0 {
		t.Errorf("未闭合旧格式不应归入思考块, got %d", len(thinks3))
	}
}

func TestThinkFold_StreamingStart(t *testing.T) {
	// 流式开头识别：检测到 <thinking> 开头立即识别思考内容，不输出正文
	streamBuf := "<thinking>用户想"

	body, thinks := splitThinkTags(streamBuf)
	if body != "" {
		t.Errorf("流式思考开头不应输出正文, got %q", body)
	}
	if len(thinks) != 1 {
		t.Fatalf("应识别到思考内容, got %d", len(thinks))
	}
	// 继续流式
	streamBuf += "知道文件内容"
	body, thinks = splitThinkTags(streamBuf)
	if body != "" {
		t.Errorf("思考未结束时不应输出正文, got %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "用户想知道文件内容" {
		t.Errorf("思考内容累积错误: %v", thinks)
	}
	// 结束标签到达后才输出正文
	streamBuf += "</thinking>现在我来读取文件"
	body, thinks = splitThinkTags(streamBuf)
	if body != "现在我来读取文件" {
		t.Errorf("结束标签后应输出正文, got %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "用户想知道文件内容" {
		t.Errorf("思考内容错误: %v", thinks)
	}
}

func TestThinkFold_ModelSpacePrefixFormat(t *testing.T) {
	// 模型层 deepseek.go 实际输出格式：<|reasoning|> + reasoning + <|/reasoning|> + content
	streamBuf := "<|reasoning|>用户想知道文件内容，先读取一下<|/reasoning|>我来读取这个文件。"

	body, thinks := splitThinkTags(streamBuf)
	if body != "我来读取这个文件。" {
		t.Errorf("body expected '我来读取这个文件。', got %q", body)
	}
	if len(thinks) != 1 {
		t.Fatalf("expected 1 think, got %d", len(thinks))
	}
	if thinks[0] != "用户想知道文件内容，先读取一下" {
		t.Errorf("think content mismatch: %q", thinks[0])
	}
}

func TestThinkFold_ModelSpacePrefixStreaming(t *testing.T) {
	// 流式增量：模型发 "<|reasoning|>" 开头 → 立即识别思考
	streamBuf := "<|reasoning|>"

	body, thinks := splitThinkTags(streamBuf)
	if body != "" {
		t.Errorf("流式思考开头不应输出正文, got %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "" {
		t.Errorf("应识别到思考（内容为空）, got %v", thinks)
	}

	// 思考内容到达
	streamBuf += "先分析"
	_, thinks = splitThinkTags(streamBuf)
	if len(thinks) != 1 || thinks[0] != "先分析" {
		t.Errorf("思考内容累积错误: %v", thinks)
	}

	// <|/reasoning|> 结束 → 输出正文
	streamBuf += "<|/reasoning|>正文来了"
	body, thinks = splitThinkTags(streamBuf)
	if body != "正文来了" {
		t.Errorf("结束标记后应输出正文, got %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "先分析" {
		t.Errorf("思考内容错误: %v", thinks)
	}
}

func TestThinkFold_ModelSpacePrefixOnlyReasoning(t *testing.T) {
	// 只有思考无正文（模型调用工具时）
	streamBuf := "<|reasoning|>需要调用工具读取文件<|/reasoning|>"

	body, thinks := splitThinkTags(streamBuf)
	if body != "" {
		t.Errorf("body should be empty, got %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "需要调用工具读取文件" {
		t.Errorf("thinks mismatch: %v", thinks)
	}
}

func TestThinkFold_ModelSpacePrefixPathNotMatched(t *testing.T) {
	// 路径中的 thinking 前面是 / 不是空格 → 不误判（需求4）
	streamBuf := "请读取 /home/user/thinking_project/notes.txt 这个文件"

	body, thinks := splitThinkTags(streamBuf)
	if body != streamBuf {
		t.Errorf("路径中的 thinking 不应被过滤, body=%q", body)
	}
	if len(thinks) != 0 {
		t.Errorf("路径不应被识别为思考块, thinks=%v", thinks)
	}
}

func TestThinkFold_ModelSpacePrefixEnglishResponse(t *testing.T) {
	// 思考内容中的 response 在正文中不应误判为结束标记（旧空格格式已废弃）
	streamBuf := "<|reasoning|>思考<|/reasoning|>API response handling 这是正文"

	body, thinks := splitThinkTags(streamBuf)
	if body != "API response handling 这是正文" {
		t.Errorf("body mismatch: %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "思考" {
		t.Errorf("thinks mismatch: %v", thinks)
	}
}

func TestThinkFold_ModelSpacePrefixPathResponse(t *testing.T) {
	// 模型层标记格式：<|/reasoning|> 后正文
	streamBuf := "<|reasoning|>读取 /api/response.json<|/reasoning|>完成"

	body, thinks := splitThinkTags(streamBuf)
	if body != "完成" {
		t.Errorf("body mismatch: %q", body)
	}
	if len(thinks) != 1 || thinks[0] != "读取 /api/response.json" {
		t.Errorf("thinks mismatch: %v", thinks)
	}
}

func TestThinkFold_BodyEnglishWordsNotFiltered(t *testing.T) {
	// 回归：正文中的英文单词 thinking/response 不应被过滤（修复显示截断问题）
	streamBuf := "让我检查这些点：1. main.go 中 bubbletea Program 选项 2. handleCmd 的 enter 逻辑 3. 检查 streamMsg 的 response 处理 thinking 环节"

	body, thinks := splitThinkTags(streamBuf)
	if body != streamBuf {
		t.Errorf("正文中的 thinking/response 不应被过滤, body=%q", body)
	}
	if len(thinks) != 0 {
		t.Errorf("不应识别出思考块, thinks=%v", thinks)
	}
}
