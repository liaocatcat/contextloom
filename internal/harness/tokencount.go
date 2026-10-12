package harness

// TextTokens 纯文本 token 估算（不含 JSON 序列化 overhead）。
// 与 EstimateTokens 的差异：不叠加每条消息 8 tokens 结构开销——供
// ctxmgr.MessageTokens 按"整条消息只计一次结构开销"复用，避免一条含多个
// 字段（content/name/ToolCallID/多个 tool_calls）的消息被重复计入 overhead
// （旧实现每字符串 +8，工具密集对话结构开销被放大 2-3 倍 → 过早压缩/强制收尾）。
func TextTokens(s string) int {
	tokens := 0.0
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF: // CJK 统一表意
			tokens += 0.9
		case r >= 0x3000 && r <= 0x30FF: // CJK 标点/假名
			tokens += 0.8
		case r >= 0x1F000: // Emoji/补充字符
			tokens += 2.5
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			tokens += 0.1
		case r < 0x80: // ASCII 可见
			tokens += 0.3
		default:
			tokens += 1.0
		}
	}
	return int(tokens)
}

// EstimateTokens CJK-aware token 估算（借鉴业界开源 Agent 框架的精确计数器）。
// 无需 tiktoken 依赖，按码点分语言赋权。
//
// 系数依据（DeepSeek 及主流中文 LLM tokenizer 实测口径）：
//   - 中文约 0.7-0.8 token/字（BPE 常将高频双字词合并为单 token）；
//   - 英文约 0.28-0.3 token/字符（单词级 BPE，空格并入相邻 token）；
//   - CJK 标点/假名与中文接近，略低。
//
// 安全余量设计（宁高勿低防溢出）：
//   - CJK 取 0.9：比实测 0.7-0.8 高约 12-28%，压缩/收尾阈值早于真实溢出触发；
//   - ASCII 取 0.3：比实测 0.28-0.3 高约 0-7%；
//   - Emoji 2.5：代理对双码点，BPE 实际常更高；
//   - 整体对混合文本偏高估 10-30%：保证上下文预算判断"宁压缩过早，不真实溢出"，
//     避免 API 400 与强制收尾。
//
// 返回 = TextTokens(s) + 8（单条字符串的 JSON 序列化 overhead：role/content 键、
// 引号、逗号、换行，实测约 6-10 tokens 取中值 8）。多字段消息的 overhead 归并
// 由 ctxmgr.MessageTokens 负责（每消息一次），避免重复计入。
func EstimateTokens(s string) int {
	return TextTokens(s) + 8
}

// EstimateMessagesTokens 估算消息列表的总 token 数
func EstimateMessagesTokens(messages []string) int {
	total := 0
	for _, msg := range messages {
		total += EstimateTokens(msg)
	}
	return total
}
