// Package ctxmgr 上下文预算管理：token 预算驱动的渐进式分层降级。
//
// 设计原则（替代旧版"按条数断崖截断"）：
//  1. 按 token 预算触发，条数只是辅助；
//  2. 渐进式压缩：完整保留 → 前缀截断 → 丢弃最旧 → 滚动摘要兜底；
//  3. 永不丢弃最近 keepRecent 条完整消息；
//  4. 被丢弃内容以滚动摘要（[历史摘要] 注入）保留要点，避免断崖式失忆。
package ctxmgr

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// Action 预算决策动作
type Action int

const (
	// ActionNone 预算充足，无需处理
	ActionNone Action = iota
	// ActionCompact 轻度压缩：非最近消息截断为前缀
	ActionCompact
	// ActionHardCompact 重度压缩：截断 + 丢弃最旧 + 滚动摘要
	ActionHardCompact
)

// EvictFunc 被压缩内容回调：当消息因压缩被截断/丢弃、原文不再完整存在于对话时调用，
// 传入该消息的原始完整内容（未截断），供调用方写入检索索引（如 Harness），
// 使 AI 之后可通过 harness_search 找回被压缩的原文，避免"压缩即永久丢失"。
type EvictFunc func(m types.Message)

// TokenBudget 预算控制器：唯一决策者，所有上下文策略（Runtime/Agent/Backend）统一接入。
type TokenBudget struct {
	MaxTokens    int     // 模型上下文窗口上限（= cfg.Model.MaxContext）
	CompactRatio float64 // 达到该用量开始前缀截断（默认 0.70）
	HardRatio    float64 // 达到该用量强制丢弃最旧（默认 0.85）
	SafetyRatio  float64 // 发送前兜底红线（默认 0.92）
	KeepRecent   int     // 至少保留的完整消息条数（默认 8）

	// Summary 滚动摘要：被丢弃消息的要点累积（上限 1200 字符），
	// 由调用方在压缩后以 system 消息注入对话，避免断崖式失忆。
	Summary string

	// audit 压缩审计：记录每次压缩动作（时间+动作+影响），
	// 供 /ctx 命令展示"压缩了什么"，解决"压缩信息丢失不可审计"的共性缺点。
	audit []string
}

// addAudit 追加一条压缩审计记录（保留最近 20 条）。
func (b *TokenBudget) addAudit(format string, args ...any) {
	b.audit = append(b.audit, fmt.Sprintf(format, args...))
	if len(b.audit) > 20 {
		b.audit = b.audit[len(b.audit)-20:]
	}
}

// AuditLog 返回压缩审计记录副本（供 /ctx 展示）。
func (b *TokenBudget) AuditLog() []string {
	out := make([]string, len(b.audit))
	copy(out, b.audit)
	return out
}

// Clone 返回预算副本（锁外压缩用）：阈值/上限/KeepRecent 深拷贝，
// Summary 浅拷贝字符串（不可变）；audit 不拷贝（压缩审计只记主线压缩）。
// 用途：AddToHistory 锁外压缩路径——压缩耗时期间若配置热更新替换了
// rt.ctxBudget，锁外仍持有引用的就是旧对象；用副本压缩、回写前把副本
// Summary 合并回现役预算，可避免"压缩结果写入已被替换的旧预算"的
// 丢失更新（lost-update），也避免压缩期间修改共享预算的审计/摘要字段。
func (b *TokenBudget) Clone() *TokenBudget {
	if b == nil {
		return nil
	}
	cp := *b
	cp.audit = nil
	return &cp
}

// MergeAudit 合并一条来自锁外压缩副本的审计记录（去重后追加）。
// 锁外压缩（AddToHistory 用 Clone 副本）期间产生的动作审计最终由
// Runtime.MergeSummaryInto 合并回现役预算，保证 /ctx 审计视图完整。
func (b *TokenBudget) MergeAudit(line string) {
	if line == "" {
		return
	}
	for _, a := range b.audit {
		if a == line {
			return
		}
	}
	b.audit = append(b.audit, line)
	if len(b.audit) > 20 {
		b.audit = b.audit[len(b.audit)-20:]
	}
}

// NewTokenBudget 构造预算控制器（默认阈值），maxTokens<=0 表示无预算限制。
func NewTokenBudget(maxTokens int) *TokenBudget {
	return &TokenBudget{
		MaxTokens:    maxTokens,
		CompactRatio: 0.70,
		HardRatio:    0.85,
		SafetyRatio:  0.92,
		KeepRecent:   8,
	}
}

// ApplyConfig 应用配置项（保留默认值的字段不变）
func (b *TokenBudget) ApplyConfig(compactRatio, hardRatio, safetyRatio float64, keepRecent int) {
	if compactRatio > 0 && compactRatio < 1 {
		b.CompactRatio = compactRatio
	}
	if hardRatio > 0 && hardRatio < 1 {
		b.HardRatio = hardRatio
	}
	if safetyRatio > 0 && safetyRatio < 1 {
		b.SafetyRatio = safetyRatio
	}
	if keepRecent > 0 {
		b.KeepRecent = keepRecent
	}
	// 保证阈值单调：compact < hard < safety
	if b.CompactRatio >= b.HardRatio {
		b.HardRatio = b.CompactRatio + (1-b.CompactRatio)/2
	}
	if b.HardRatio >= b.SafetyRatio {
		b.SafetyRatio = b.HardRatio + (1-b.HardRatio)/2
	}
}

// Usage 当前用量比例 [0,1]
func (b *TokenBudget) Usage(tokens int) float64 {
	if b.MaxTokens <= 0 {
		return 0
	}
	return float64(tokens) / float64(b.MaxTokens)
}

// Decide 决策：返回应执行的压缩动作。
func (b *TokenBudget) Decide(current, msgCount int) Action {
	if msgCount <= b.KeepRecent {
		return ActionNone
	}
	usage := b.Usage(current)
	switch {
	case usage > b.HardRatio:
		return ActionHardCompact
	case usage > b.CompactRatio:
		return ActionCompact
	}
	return ActionNone
}

// SafetyExceeded 是否超过安全红线（发送前兜底）
func (b *TokenBudget) SafetyExceeded(tokens int) bool {
	return b.Usage(tokens) > b.SafetyRatio
}

// ExceedsCompactRatio 是否超过轻度压缩阈值（达到该用量开始前缀截断）。
// ForceCompact 的短路判据用本方法而非 SafetyExceeded：占用超过压缩阈值时
// 即使消息条数 ≤ keepRecent 也必须允许截断（巨型单条消息可削长度），
// keepRecent 只保护"不丢弃"，不保护"不截断"（见 ForceCompactWithEvict 注释）。
func (b *TokenBudget) ExceedsCompactRatio(tokens int) bool {
	return b.Usage(tokens) > b.CompactRatio
}

// MessageTokens 估算单条消息 token（覆盖完整消息结构）。
//
// 覆盖面（修复前只算 string content，导致两类偏差）：
//  1. tool_calls 参数 JSON 完全漏算——工具循环中 write_file 等调用的 arguments
//     可达数千 token，低估 → 真实请求溢出 → API 400；
//  2. Name / ToolCallID 短标识符漏算——量小但应计入。
//  3. 多模态图片片段（image_url data URI）漏算——base64 可达 MB 级，
//     低估会让压缩永不触发 → API 400；现按每张图片固定上限 384 token
//     计入（DeepSeek 官方缩放规则：~800×800 总像素、每张 <384 token，
//     与 base64 长度无关——旧实现按 4 字符/token 估算把单张 5MB 图算成
//     ~167 万 token，瞬间打爆预算触发压缩、图片被降级，见 imagePartTokens）。
//
// 估算组成：文本内容（string 或 []ContentPart 折算纯文本）+ 思维链
// （ReasoningContent，DeepSeek thinking 模式历史必须原样回传、真实占用预算）+
// 图片片段 + Name + ToolCallID + 每个 tool_call 的 name/arguments +
// 消息级结构开销 8 + 每个 tool_call 6。结构开销【整条消息只计一次】（用
// harness.TextTokens 取纯文本估算）：旧实现每字符串叠 EstimateTokens（内含
// +8），一条含多个字段的消息被重复计入 overhead 2-3 倍 → 工具密集对话估算
// 虚高 → 过早压缩/强制收尾。
//
// 思维链漏算 = 右下角 ctx 不变的直接根因：SplitReasoning 把思维链剥离到
// ReasoningContent 后，本函数只计 TextContent（正文）——thinking 模型每轮
// 回复正文短、思维链长（可达数千 token），纯思考轮甚至正文为空 → 空消息
// 早退返回 0，used 几乎不增长，状态栏 ctx 纹丝不动。压缩阈值判断也基于
// MessagesTokens，同源低估会让压缩永不触发 → 实际超限 → API 400。
func MessageTokens(m types.Message) int {
	// 空消息（无内容、无思维链、无标识、无工具调用）不产生任何 token
	if m.TextContent() == "" && m.ReasoningContent == "" && m.ImagePartCount() == 0 && m.Name == "" && m.ToolCallID == "" && len(m.ToolCalls) == 0 {
		return 0
	}
	total := 8 // 消息级 JSON 序列化 overhead（role/content 键、引号、逗号），只计一次
	// 1. 文本内容：string 直算；[]ContentPart 按 TextContent 折算（多模态文本部分）
	if s := m.TextContent(); s != "" {
		total += harness.TextTokens(s)
	}
	// 1.5 思维链：与正文同规则计入（thinking 模式回传 reason_content 真实占用预算）
	if m.ReasoningContent != "" {
		total += harness.TextTokens(m.ReasoningContent)
	}
	// 2. 图片片段：每张固定上限 384 token（官方缩放规则）+ 85 结构开销。
	//    兼容 JSON 反序列化形态 []any（检查点恢复后 TextContent 仍可折算文本，
	//    但图片片段必须单独计数，否则恢复后的多模态历史被低估）。
	if parts, ok := types.ContentPartsOf(m.Content); ok {
		for _, p := range parts {
			if p.Type == "image_url" && p.ImageURL != nil {
				total += imagePartTokens(p.ImageURL.URL)
			}
		}
	}
	// 3. Name / ToolCallID：短标识符，估算计入
	if m.Name != "" {
		total += harness.TextTokens(m.Name)
	}
	if m.ToolCallID != "" {
		total += harness.TextTokens(m.ToolCallID)
	}
	// 4. ToolCalls：函数名 + 参数 JSON（write_file 的 content 参数可达数千 token），
	//    每条另加 6 结构开销（json id/type/function 键 + 引号逗号）
	for _, tc := range m.ToolCalls {
		if tc.Function.Name != "" {
			total += harness.TextTokens(tc.Function.Name)
		}
		if tc.Function.Arguments != "" {
			total += harness.TextTokens(tc.Function.Arguments)
		}
		total += 6
	}
	return total
}

// imagePartTokens 估算单个图片片段的 token 开销。
//
// 按 DeepSeek 官方视觉文档（https://api-docs.deepseek.com/zh-cn/guides/vision）：
// 图片在进入模型前会被自动缩放——总像素小于约 384×384 的放大保持长宽比、
// 更大的缩小至约 800×800 总像素，因此【每张图片消耗的 token 数存在上限 384】，
// 与文件大小 / base64 长度无关。5000×5000 与 200×200 的图片经缩放后 token 相同。
//
// 旧实现按 len(url)/4（base64 约 4 字符/token）估算，把 MB 级 data URI 当成
// 文本上下文计数——一张 5MB 图估算 ~167 万 token，瞬间打爆 204k/1M 预算 →
// 触发自动压缩 → 图片被降级为占位文本 → 用户"上传图片就压缩、图也识别不了"。
// 修正为固定上限估算：不随 base64 长度膨胀，预算统计与实际计费一致。
//
// 加 85 结构开销（JSON 键 image_url/url）保持与历史审计口径兼容。
func imagePartTokens(url string) int {
	if url == "" {
		return 0
	}
	// 每张图片 token 上限（官方缩放规则：~800×800 总像素，上限 384）。
	const maxTokensPerImage = 384
	return 85 + maxTokensPerImage
}

// MessagesTokens 估算消息列表总 token
func MessagesTokens(msgs []types.Message) int {
	total := 0
	for _, m := range msgs {
		total += MessageTokens(m)
	}
	return total
}

// TruncateForBudget 单条消息字符级截断（按 rune，避免切断多字节字符）
func TruncateForBudget(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars]) + "…"
}

// truncateArgumentsJSON 值级截断 tool_calls 的 arguments JSON，保证截断后仍是合法
// JSON 字符串（OpenAI 兼容 API 对 tool_calls.arguments 要求可解析 JSON，粗暴截断
// 到一半会 400）。
//
// 策略：解析为对象 → 对最长的字符串值按剩余预算截断 → 重新序列化；解析失败或
// 序列化后仍超预算时，整体替换为最小合法 JSON。历史 tool_calls 的参数对后续轮次
// 通常不再必需（模型依赖 tool 结果），截断值不会破坏配对（ID/name 原样保留）。
func truncateArgumentsJSON(args string, maxChars int) string {
	if len([]rune(args)) <= maxChars {
		return args
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err == nil {
		// 找最长字符串值（通常是 content/path 等大参数）
		var maxKey string
		maxLen := 0
		for k, v := range obj {
			if s, ok := v.(string); ok && len([]rune(s)) > maxLen {
				maxKey, maxLen = k, len([]rune(s))
			}
		}
		if maxKey != "" {
			// 预算 = 总预算 − 其他字段的序列化开销估算（键名+引号+冒号+逗号+值）
			other := 0
			for k, v := range obj {
				if k == maxKey {
					continue
				}
				b, _ := json.Marshal(v)
				other += len([]rune(string(b))) + len([]rune(k)) + 5
			}
			budget := (maxChars - other - 4) * 9 / 10 // 键值开销安全系数（花括号+引号+冒号）
			if budget < 32 {
				budget = 32 // 防止截断到无意义长度；再小则走整体替换兜底
			}
			obj[maxKey] = TruncateForBudget(obj[maxKey].(string), budget)
			if out, err := json.Marshal(obj); err == nil && len([]rune(string(out))) <= maxChars {
				return string(out)
			}
		}
	}
	// 兜底：最小合法 JSON（截断信息可审计，结构合法不 400）
	return `{"truncated":true,"note":"arguments过长已压缩"}`
}

// prefixLimit 计算非最近消息的前缀保留长度：
// 原长 1/3，下限 60 字符，上限 400 字符（渐进式，旧消息保留更少）。
func prefixLimit(s string) int {
	n := len([]rune(s)) / 3
	if n < 60 {
		n = 60
	}
	if n > 400 {
		n = 400
	}
	return n
}

// containsImagePart 判断消息内容是否携带图片片段（兼容 []ContentPart 与
// JSON 反序列化的 []any 形态）。
func containsImagePart(m types.Message) bool {
	return m.ImagePartCount() > 0
}

// degradeImageParts 把消息中的图片片段替换为文本占位，返回替换后的消息与
// 是否发生替换。文本片段原样保留；全图消息降级为纯占位文本。
//
// 目的：图片 data URI 可达 MB 级，上下文预算压缩必须优先砍图（图片重传
// 成本远高于文本），否则压缩后仍超红线 → API 400。降级为占位文本后模型
// 知道原位置曾有图片（可通过 harness_search 找回压缩原文中的路径信息）。
func degradeImageParts(m types.Message) (types.Message, bool) {
	parts, ok := types.ContentPartsOf(m.Content)
	if !ok || !containsImagePart(m) {
		return m, false
	}
	var texts []string
	for _, p := range parts {
		if p.Type == "text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	np := make([]types.ContentPart, 0, 2)
	if len(texts) > 0 {
		np = append(np, types.ContentPart{Type: "text", Text: strings.Join(texts, "")})
	}
	np = append(np, types.ContentPart{Type: "text", Text: types.ImageOmittedPlaceholder})
	m.Content = np
	return m, true
}

// StripImageDataURI 把消息中的图片 data URI 片段降级为文本占位（保留文本），
// 返回替换后的消息与是否发生替换。与 degradeImageParts 同语义，导出供
// 持久化层（压缩快照落盘 / 事件日志）使用——快照携带 MB 级 dataURI 会
// 让 session.json 膨胀到几十 MB，之后 LoadSession 读盘解析巨文件卡死
// （"压缩后历史记录不能加载"根因之一）；图片在事件日志已有 [图片: 路径]
// 锚点，重载时按路径重建，快照无需携带图。
func StripImageDataURI(m types.Message) (types.Message, bool) {
	return degradeImageParts(m)
}

// ForceCompact 强制压缩（手动 /compress 命令用）：无论当前用量是否超阈值，
// 都执行渐进式压缩（截断旧消息→丢弃最旧→滚动摘要），保证状态栏 ctx 占比实时下降。
// 实现：临时将预算上限压到当前用量的 60%（usage≈1.67，三层阈值全部触发），
// 同时把 KeepRecent 临时降到当前条数一半（下限 1，system 消息仍受保护恒保留）
// ——手动压缩不受条数限制，可一直压到只剩 1 条完整消息（恢复旧版
// "压缩不受限"行为）。压缩后恢复原预算与原 KeepRecent。
// 调用方需持有 historyMu 写锁（无并发风险）。
func (b *TokenBudget) ForceCompact(msgs []types.Message) ([]types.Message, bool) {
	return b.ForceCompactWithEvict(msgs, nil)
}

// ForceCompactWithEvict 同 ForceCompact，额外支持 evict 回调（被压缩原文入检索索引）。
//
// ⚠️ 手动压缩语义：**不受条数限制，可一直压缩到只剩 1 条完整消息**——
// 每次调用把 KeepRecent 临时降到当前条数一半（下限 1，system 消息仍受保护
// 恒保留；被丢弃内容累积进滚动摘要），渐进式逐步压到终点。这是"很早以前
// 压缩不受限、可以一直压到只剩一条记录"的旧版行为（用户实锤回归）。
// 自动压缩（runtime 直接调 CompactToBudgetWithEvict 的路径）不受影响，
// 仍保留 keepRecent 条完整消息保护最近上下文——两者互不干扰：
// 手动压缩求"压得动"，自动压缩求"别失忆"。
func (b *TokenBudget) ForceCompactWithEvict(msgs []types.Message, evict EvictFunc) ([]types.Message, bool) {
	// 短路条件：仅当"消息确实无需压缩"时才跳过——条数 ≤ keepRecent **且**
	// 未超压缩阈值（CompactRatio）时说明消息都短小、没有可释放空间，直接跳过。
	//
	// ⚠️ 不能用 SafetyExceeded(92%) 做短路判据（历史缺陷，用户实锤）：
	// keepRecent 的本意是"不丢弃最近 N 条"，但旧代码把它偷换成"不压缩最近
	// N 条"——当历史恰好 ≤ keepRecent 条巨型消息（长文本/大工具结果，单条
	// 可达 2 万 token）且占用 60~92%（未超安全红线）时，ForceCompact 直接
	// 短路返回 released==0，用户看到"无需压缩：已压至精简态（无可再释放）"
	// 却占用 76.89%（156855/204000，真实反馈）。截断（step1/3/4 只削单条
	// 长度、保留全部消息）完全不受 keepRecent 约束，本应始终可执行。
	// 修复：短路阈值从 SafetyRatio(92%) 收紧到 CompactRatio(70%)——只有
	// 占用低于压缩阈值时才算"确实无需压缩"；76.89% 占用必然进入压缩流程，
	// 由 step1/3/4 截断巨型消息释放空间（消息条数保持不变，不违背 keepRecent
	// "不丢弃"语义）。
	if b == nil || b.MaxTokens <= 0 || len(msgs) == 0 {
		return msgs, false
	}
	// 手动压缩短路条件：仅当"压无可压"才跳过——消息已 ≤ 1 条（旧版"可压缩
	// 到只剩一条记录"的终点）且未超安全红线。不能沿用自动压缩的
	// len<=KeepRecent && 占用低（KeepRecent=8）：那会让压到 8 条以内、
	// 占用低于 70% 的历史永远压不动（用户"压缩不受限"痛点回归，实测
	// 第 4 轮 6 条 changed=false 卡死）。手动压缩语义 = 只要还能压就压：
	//  - 1 条消息（占用低）→ 压无可压，短路；
	//  - 2 条巨型消息（超红线）→ 仍需 step4 折半压缩（超红线不能放行）。
	if len(msgs) <= 1 && !b.SafetyExceeded(MessagesTokens(msgs)) {
		return msgs, false
	}
	used := MessagesTokens(msgs)
	if used <= 0 {
		return msgs, false
	}
	// 手动压缩渐进式：每次只压掉一半（KeepRecent = len/2，下限 1），
	// system 消息始终受保护恒保留。这样：
	//  1. 恢复旧版"压缩不受限、可一直压到只剩一条记录"语义（连按多次
	//     每次条数减半，直到剩 system+1 条）；
	//  2. 每次点击都真实释放 token——若一次压到底（KeepRecent=1），
	//     第二次再点就"无需压缩"（用户"只能压缩一次"痛点回归，实测失败）。
	// 三个阈值全部临时调 0：
	//  - CompactRatio=0：绕过 CompactToBudgetWithEvict 开头总短路
	//    （Usage<=CompactRatio 直接返回）——渐进多轮后 token 已很低（<1%），
	//    不调低它会永远短路（实测第 4 轮 6 条卡死）；
	//  - HardRatio=0：step2 丢弃循环只看条数（len>keep），不受 token 占比
	//    限制——否则 token 压到 <0.85 后 step2 提前退出，压不到目标条数；
	//  - SafetyRatio=0：step3 二次截断与 step4 折半兜底无条件执行——
	//    2 条巨型消息（超红线）也能折半压到下限。
	// 压缩完成（defer）后恢复，不影响自动压缩路径的阈值语义。
	savedKeep := b.KeepRecent
	savedHard := b.HardRatio
	savedCompact := b.CompactRatio
	savedSafety := b.SafetyRatio
	half := len(msgs) / 2
	if half < 1 {
		half = 1
	}
	b.KeepRecent = half
	b.HardRatio = 0    // 0：step2 丢弃循环只看条数（len>keep），不受 token 占比限制
	b.CompactRatio = 0 // 0：绕过总短路
	b.SafetyRatio = 0  // 0：step3/step4 无条件截断/折半
	defer func() {
		b.KeepRecent = savedKeep
		b.HardRatio = savedHard
		b.CompactRatio = savedCompact
		b.SafetyRatio = savedSafety
	}()
	saved := b.MaxTokens
	b.MaxTokens = used * 6 / 10 // 60% → usage≈1.67，必触发截断/丢弃/摘要
	if b.MaxTokens < 1 {
		b.MaxTokens = 1
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, evict)
	b.MaxTokens = saved
	return out, changed
}

// CompactToBudget 将消息列表压缩到预算内（渐进式分层），不回调 evict。
func CompactToBudget(msgs []types.Message, b *TokenBudget) ([]types.Message, bool) {
	return CompactToBudgetWithEvict(msgs, b, nil)
}

// CompactToBudgetWithEvict 将消息列表压缩到预算内（渐进式分层）：
//
//	第 1 步：最近 keepRecent 条完整保留，其余消息截断为前缀（原长 1/3）；
//	第 2 步：仍超硬上限 → 从最旧开始丢弃（至少保留 keepRecent 条），
//	        被丢弃内容累积进 b.Summary 滚动摘要；
//	第 3 步：仍超安全红线 → 对保留消息进一步截断（每条至少保留 120 字符）；
//	第 4 步：逐条折半截断最大消息直至低于红线（content 下限 100 字符；
//	        content 到下限后改折半 tool_calls 参数，JSON 值级截断保持合法）。
//
// evict 回调：任何消息被截断/丢弃导致原文不再完整存在于对话时，
// 以原始完整内容调用 evict（每条完整原文只回调一次，避免重复入索引）。
// 返回压缩后的消息与是否发生压缩。不修改原切片。
// 每次压缩动作都会记录到 b.AuditLog()，供 /ctx 审计展示。
//
// ⚠️ 压缩红线（role=system 消息保护）：
//   - system 消息（第 0 条，含任务指令/策略/协议）在 step1 前缀截断、
//     step2 丢弃最旧、step3 二次截断中一律【跳过】，恒保留原样；
//   - step4 折半兜底优先截断非 system 最大消息；仅当所有非 system 消息
//     都已 ≤100 字符仍超限时，才允许折半 system 作为最后手段
//     （下限 200 字符，保证不 panic、不无限循环）。
//
// ⚠️ 截断重建完整性：任何截断重建都必须完整保留 m.Name / m.ToolCallID /
// m.ToolCalls 字段——tool 消息截断后丢失 ToolCallID 会被
// StripOrphanToolMessages 当孤儿剥离，导致 assistant tool_calls 悬空
// （无 tool 响应）→ OpenAI 兼容 API 400。tool_calls arguments 截断采用
// truncateArgumentsJSON（值级截断保持 JSON 合法），ID/name 原样保留。
func CompactToBudgetWithEvict(msgs []types.Message, b *TokenBudget, evict EvictFunc) ([]types.Message, bool) {
	// 注意：这里【不能】用 len(msgs) <= b.KeepRecent 短路——单条巨消息
	// （如 100KB 文件读取结果）在条数 ≤ keepRecent 时同样可能超红线，
	// 短路会使 step4 折半兜底成为死代码。条数守卫由各步骤自身承担
	// （step1 len>keep、step2 len>keep、step3 len>1），此处仅拦空列表。
	if b == nil || b.MaxTokens <= 0 || len(msgs) == 0 {
		return msgs, false
	}
	if b.Usage(MessagesTokens(msgs)) <= b.CompactRatio {
		return msgs, false
	}
	changed := false
	keep := b.KeepRecent
	if keep < 1 {
		keep = 1 // 防御：KeepRecent 非法时至少保留 1 条，防止 step2 全丢
	}
	dropped := 0

	// 原始完整副本 + 当前下标→原始下标映射：
	// 压缩过程中消息可能被截断/丢弃，origIdx 保证任何一步都能取到该消息的原始完整内容。
	original := make([]types.Message, len(msgs))
	copy(original, msgs)
	origIdx := make([]int, len(msgs))
	for i := range origIdx {
		origIdx[i] = i
	}

	// evictOnce 每条原始消息只回调一次完整原文（无论被截断几次/截哪个字段），
	// 避免同一原文重复入检索索引。
	evicted := make(map[int]bool) // origIdx → 已回调
	evictOnce := func(oi int) {
		if evict == nil || evicted[oi] {
			return
		}
		evicted[oi] = true
		evict(original[oi])
	}

	// ── 第 1 步：前缀截断（system 消息恒跳过，保留原样）──
	if len(msgs) > keep {
		var out []types.Message
		truncated := 0
		for i, m := range msgs {
			if i < len(msgs)-keep && m.Role != "system" {
				// 旧消息携带图片：先降级为占位（砍掉 MB 级 data URI 大头），
				// 文本部分同样受前缀限长约束，防多轮识图历史撑爆上下文。
				if deg, ok := degradeImageParts(m); ok {
					evictOnce(origIdx[i])
					if p, ok2 := types.ContentPartsOf(deg.Content); ok2 && len(p) > 0 {
						p[0].Text = TruncateForBudget(p[0].Text, prefixLimit(p[0].Text))
						deg.Content = p
					}
					out = append(out, deg)
					truncated++
					changed = true
					continue
				}
				if s, ok := m.Content.(string); ok && s != "" {
					limit := prefixLimit(s)
					// 思维链（ReasoningContent）同样计入 MessagesTokens（thinking 模型
					// 每轮思维链可达数千 token、正文往往仅几百字符）——若只截正文，
					// 压缩后占用几乎不变，released==0 → 用户"76.89% 高占用却压不动"
					// （真实根因：压缩器曾对思维链是盲区）。因此正文或思维链任一超限
					// 都触发截断，两个字段一起削到 limit。
					reasonLong := m.ReasoningContent != "" && len([]rune(m.ReasoningContent)) > limit
					if len([]rune(s)) > limit || reasonLong {
						evictOnce(i) // 此时 m == original[i]（尚未改动），回调完整原文
						// 重建必须保留 Name/ToolCallID/ToolCalls，防 tool 配对断裂（API 400）
						m.Content = TruncateForBudget(s, limit)
						if reasonLong {
							m.ReasoningContent = TruncateForBudget(m.ReasoningContent, limit)
						}
						out = append(out, m)
						truncated++
						changed = true
						continue
					}
				}
			}
			out = append(out, m)
		}
		if truncated > 0 {
			b.addAudit("前缀截断 %d 条旧消息（保留原长 1/3；system 消息受保护）", truncated)
		}
		msgs = out
	}

	// ── 第 2 步：丢弃最旧（累积滚动摘要；system 与 [历史摘要] 消息受保护不被丢弃）──
	for len(msgs) > keep && b.Usage(MessagesTokens(msgs)) > b.HardRatio {
		// 找到最旧的"非 system、非摘要"消息丢弃
		dropIdx := 0
		for dropIdx < len(msgs)-keep && (isSummaryMsg(msgs[dropIdx]) || msgs[dropIdx].Role == "system") {
			dropIdx++
		}
		if dropIdx >= len(msgs)-keep {
			break // 只剩 system/摘要+最近消息，无法再丢
		}
		evictOnce(origIdx[dropIdx])
		b.addToSummary(msgs[dropIdx])
		msgs = append(msgs[:dropIdx], msgs[dropIdx+1:]...)
		origIdx = append(origIdx[:dropIdx], origIdx[dropIdx+1:]...)
		changed = true
		dropped++
	}
	if dropped > 0 {
		b.addAudit("丢弃 %d 条最旧消息并累积滚动摘要（system 消息受保护）", dropped)
	}

	// ── 第 3 步：仍超红线 → 二次截断（除最后一条外全部限长 300 字符；system 恒跳过）──
	if b.Usage(MessagesTokens(msgs)) > b.SafetyRatio && len(msgs) > 1 {
		var out []types.Message
		reTrunc := 0
		for i, m := range msgs {
			if i < len(msgs)-1 && m.Role != "system" { // 最后一条（最新）优先完整保留；system 恒保留原样
				if deg, ok := degradeImageParts(m); ok {
					evictOnce(origIdx[i])
					if p, ok2 := types.ContentPartsOf(deg.Content); ok2 && len(p) > 0 {
						p[0].Text = TruncateForBudget(p[0].Text, 300)
						deg.Content = p
					}
					out = append(out, deg)
					reTrunc++
					changed = true
					continue
				}
				if s, ok := m.Content.(string); ok && s != "" && len([]rune(s)) > 300 {
					evictOnce(origIdx[i])
					// 重建必须保留 Name/ToolCallID/ToolCalls，防 tool 配对断裂（API 400）
					m.Content = TruncateForBudget(s, 300)
					out = append(out, m)
					reTrunc++
					changed = true
					continue
				}
				// 思维链同样超 300 字符时截断（thinking 模型思维链是占用大头，
				// 只截正文 released≈0 → "高占用却压不动"）。
				// M10 思考保护：带保护标记的思考跳过二次截断（验证轮的"上一轮思考"是复核关键证据，结论在尾段、截前缀恰好丢结论；正文仍可削）。
				if m.ReasoningContent != "" && !m.ReasoningProtected && len([]rune(m.ReasoningContent)) > 300 {
					evictOnce(origIdx[i])
					m.ReasoningContent = TruncateForBudget(m.ReasoningContent, 300)
					out = append(out, m)
					reTrunc++
					changed = true
					continue
				}
			}
			out = append(out, m)
		}
		if reTrunc > 0 {
			b.addAudit("安全红线二次截断 %d 条消息（限长 300；system 消息受保护）", reTrunc)
		}
		msgs = out
	}

	// ── 第 4 步：最终兜底——逐条折半截断最大消息直至低于红线 ──
	// 覆盖三类"单条消息本身超大"的场景：
	//   a) content 超大（用户粘贴大文件）→ 折半 content；
	//   b) tool_calls arguments 超大（write_file 大参数，MessageTokens 已计入、
	//      旧实现无法压缩 → 压缩后仍超红线 → agent 强制收尾）→ 折半 arguments
	//      （truncateArgumentsJSON 值级截断，保持 JSON 合法、ID/name 保留）；
	//   c) 多模态图片消息超大（image_url data URI 达 MB 级，而消息条数未超
	//      keepRecent 时前几步不处理）→ 图片降级为占位文本（一次性砍掉大头），
	//      文本部分继续折半。
	// 选择策略：全局最大可压缩对象（content / 图片 / arguments），非 system 优先；
	// 下限：非 system 100 字符（content 与 arguments 同），system 200（最后手段）。
	// system 保护：仅当所有非 system 消息都到下限仍超限时，才折半 system。
	// 原始完整内容始终在 JSONL 事件日志中可回溯，不会真正丢失。
	halved := 0
	for b.Usage(MessagesTokens(msgs)) > b.SafetyRatio {
		// 选择压缩目标：返回消息下标、tool_call 下标（-1=折 content）、当前长度
		pick := func(allowSystem bool) (idx, tcIdx, size int) {
			idx, tcIdx, size = -1, -1, 0
			for i, m := range msgs {
				if !allowSystem && m.Role == "system" {
					continue
				}
				if s, ok := m.Content.(string); ok {
					if l := len([]rune(s)); l > size {
						idx, tcIdx, size = i, -1, l
					}
				}
				// 思维链参与竞争：thinking 模型占用大头在 ReasoningContent，
				// 不纳入则折半只动短文正文，released≈0（"高占用却压不动"根因）。
				// tcIdx=-2 标识"折思维链"。M10 思考保护：带保护标记的思考不参与截断目标竞争（正文仍可削）。
				if m.ReasoningContent != "" && !m.ReasoningProtected {
					if l := len([]rune(m.ReasoningContent)); l > size {
						idx, tcIdx, size = i, -2, l
					}
				}
				// 多模态消息：文本长度作为压缩尺寸依据。图片不参与竞争——
				// 旧实现按 data URI 长度（MB 级）竞争，大图永远被优先降级，
				// 而服务端实际按缩放后 384 token/张计费；图片降级只在
				// 文本/tool_calls 全部到下限后作为最后手段（见下方 switch）。
				if parts, ok := types.ContentPartsOf(m.Content); ok {
					for _, p := range parts {
						if p.Type == "text" && len([]rune(p.Text)) > size {
							idx, tcIdx, size = i, -1, len([]rune(p.Text))
						}
					}
				}
				for j, tc := range m.ToolCalls {
					if l := len([]rune(tc.Function.Arguments)); l > size {
						idx, tcIdx, size = i, j, l
					}
				}
			}
			return idx, tcIdx, size
		}
		idx, tcIdx, size := pick(false)
		floor := 100
		if idx < 0 || size <= floor {
			// 非 system 已全部到下限（或无文本）仍超限 → 最后手段：折半 system
			idx, tcIdx, size = pick(true)
			floor = 200
			if idx < 0 || size <= floor {
				break // 无可压缩消息，保证不 panic、不无限循环
			}
		}
		evictOnce(origIdx[idx])
		switch {
		case tcIdx == -2:
			// 折半思维链：thinking 模式占用大头，正文保留（正文对后续轮次价值更高）
			msgs[idx].ReasoningContent = TruncateForBudget(msgs[idx].ReasoningContent, size/2)
		case tcIdx >= 0:
			// 折半 tool_calls arguments：值级截断保持 JSON 合法；ID/name 保留 → 配对不破
			a := msgs[idx].ToolCalls[tcIdx].Function.Arguments
			msgs[idx].ToolCalls[tcIdx].Function.Arguments = truncateArgumentsJSON(a, size/2)
		case containsImagePart(msgs[idx]):
			// 图片降级为占位文本（一次性砍掉 MB 级大头），再折半文本
			deg, _ := degradeImageParts(msgs[idx])
			msgs[idx] = deg
		default:
			// 纯文本或已降级的多模态消息：折半文本
			if s, ok := msgs[idx].Content.(string); ok {
				msgs[idx].Content = TruncateForBudget(s, size/2)
				break
			}
			if parts, ok := types.ContentPartsOf(msgs[idx].Content); ok {
				var texts []string
				for _, p := range parts {
					if p.Type == "text" {
						texts = append(texts, p.Text)
					}
				}
				msgs[idx].Content = []types.ContentPart{{Type: "text", Text: TruncateForBudget(strings.Join(texts, ""), size/2)}}
			}
		}
		changed = true
		halved++
	}
	if halved > 0 {
		b.addAudit("折半截断 %d 次最大消息兜底（含图片降级与 tool_calls 参数；非 system 下限 100，system 仅最后手段 200）", halved)
	}

	// ── 第 5 步：清理孤儿 tool 消息 + 悬空 assistant tool_calls（配对断裂 → API 400）──
	beforeStrip := len(msgs)
	msgs = StripOrphanToolMessages(msgs)
	if len(msgs) != beforeStrip {
		changed = true // 清理结果必须让调用方采纳（changed && compacted != nil）
	}

	return msgs, changed
}

// StripOrphanToolMessages 移除没有对应 assistant tool_calls 的 tool 消息，
// 并清理"有 tool_calls 但 tool 响应已被压缩丢弃"的悬空 assistant 调用。
// 丢弃最旧消息后可能出现：assistant(tool_calls) 被丢、但其 tool 结果残留，
// 或 tool 结果被丢、assistant 调用残留——两者都会导致 OpenAI 兼容 API 400。
// 规则：按顺序跟踪"待兑现"的 tool_call_id；tool 消息的 ID 不在待兑现集合中即视为孤儿；
// 扫描结束时 pending 中未兑现的 ID，从其 assistant 消息的 ToolCalls 中移除该条目
// （assistant 的 Content 保留；全部移除则置空切片）。
func StripOrphanToolMessages(msgs []types.Message) []types.Message {
	type pendingCall struct {
		outIdx int // assistant 消息在 out 中的下标
		tcIdx  int // ToolCall 在 assistant.ToolCalls 中的下标
	}
	pending := make(map[string]pendingCall)
	var out []types.Message
	for _, m := range msgs {
		if len(m.ToolCalls) > 0 {
			out = append(out, m)
			oi := len(out) - 1
			for j, tc := range m.ToolCalls {
				if tc.ID != "" {
					pending[tc.ID] = pendingCall{outIdx: oi, tcIdx: j}
				}
			}
			continue
		}
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				continue // 无 ID 的 tool 消息：孤儿
			}
			if _, ok := pending[m.ToolCallID]; !ok {
				continue // 孤儿 tool 消息：assistant 调用已被丢弃
			}
			delete(pending, m.ToolCallID) // 兑现
			out = append(out, m)
			continue
		}
		out = append(out, m)
	}
	// 悬空 assistant 清理：pending 中未兑现的 tool_call_id（其 tool 响应已被
	// 压缩丢弃）从对应 assistant 消息的 ToolCalls 中移除该条目（Content 保留），
	// 全部移除则置空切片——消除"assistant 声明调用但无 tool 响应"的 API 400 风险。
	if len(pending) > 0 {
		byAssistant := make(map[int][]int) // outIdx → 待移除 tcIdx 列表
		for _, pc := range pending {
			byAssistant[pc.outIdx] = append(byAssistant[pc.outIdx], pc.tcIdx)
		}
		for oi, rm := range byAssistant {
			skip := make(map[int]bool, len(rm))
			for _, ti := range rm {
				skip[ti] = true
			}
			m := out[oi]
			keep := make([]types.ToolCall, 0, len(m.ToolCalls)-len(rm))
			for j, tc := range m.ToolCalls {
				if !skip[j] {
					keep = append(keep, tc)
				}
			}
			if len(keep) == 0 {
				keep = []types.ToolCall{} // 全部移除则置空切片
			}
			m.ToolCalls = keep
			out[oi] = m
		}
	}
	return out
}

// addToSummary 将被丢弃消息的要点（头部 80 字符）累积进滚动摘要，上限 1200 字符。
// 摘要消息本身（[历史摘要] 前缀）不参与累积，避免反馈循环。
func (b *TokenBudget) addToSummary(m types.Message) {
	s, ok := m.Content.(string)
	if !ok || s == "" || strings.HasPrefix(s, "[历史摘要]") {
		return
	}
	part := TruncateForBudget(s, 80)
	if b.Summary != "" {
		b.Summary += "；"
	}
	b.Summary += "[" + roleLabel(m.Role) + "] " + part
	b.Summary = TruncateForBudget(b.Summary, 1200)
}

func roleLabel(role string) string {
	switch role {
	case "user":
		return "用户"
	case "assistant":
		return "助手"
	case "tool":
		return "工具"
	default:
		return role
	}
}

// isSummaryMsg 判断是否为滚动摘要消息（受保护，压缩时优先保留）
func isSummaryMsg(m types.Message) bool {
	s, ok := m.Content.(string)
	return ok && strings.HasPrefix(s, "[历史摘要]")
}

// SummaryMessage 生成注入用的历史摘要消息（无摘要时返回 nil）
func (b *TokenBudget) SummaryMessage() *types.Message {
	if b.Summary == "" {
		return nil
	}
	return &types.Message{Role: "user", Content: "[历史摘要] " + b.Summary}
}

// ResetSummary 清空滚动摘要与审计日志（新会话时调用）
func (b *TokenBudget) ResetSummary() {
	b.Summary = ""
	b.audit = nil
}
