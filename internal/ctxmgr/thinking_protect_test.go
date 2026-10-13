package ctxmgr

import (
	"reflect"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 场景常量（验证轮：上一轮思考是复核证据，旧轮思考是可压缩对照）──
const (
	// protectThinkPhrase 长思考短语（保护目标）：340 次重复 ≈ 11220 字符。
	protectThinkPhrase = "我在深入分析这个问题的各种可能实现方案与边界条件，并权衡利弊得失。"
	// controlThinkPhrase 中思考短语（对照）：20 次重复 = 400 字符（>300，会被压缩）。
	controlThinkPhrase = "先看看这个模块的既有实现，再决定怎么改。"
	// longUserPhrase 长正文短语（正文仍可削的观察点）：20 次重复 = 400 字符。
	longUserPhrase = "执行阶段：请实现需求，先完成核心路径。"
)

// buildVerifyRoundMsgs 构造验证轮场景：
//
//	[0] system；[1] user（长正文）；[2] assistant 旧轮（中思考对照）；
//	[3] user；[4] assistant 上一轮（超长思考=保护目标）；[5] user（验证轮提示）。
func buildVerifyRoundMsgs() []types.Message {
	return []types.Message{
		{Role: "system", Content: "你是 ContextLoom 助手。"},
		{Role: "user", Content: strings.Repeat(longUserPhrase, 20)},
		{Role: "assistant", Content: "已按计划完成修改。", ReasoningContent: strings.Repeat(controlThinkPhrase, 20)},
		{Role: "user", Content: "继续推进。"},
		{Role: "assistant", Content: "已完成关键路径实现，等待验证。", ReasoningContent: strings.Repeat(protectThinkPhrase, 340)},
		{Role: "user", Content: "【验证轮】请复核上一轮产出是否正确。"},
	}
}

// buildThinkingHeavyHistory 构造 thinking 密集历史（5 轮；压缩会大幅动作）。
func buildThinkingHeavyHistory() []types.Message {
	think := strings.Repeat("我在深入思考这个问题，分析各种可能性和权衡利弊，并考虑不同的实现方案与边界情况。", 100)
	msgs := []types.Message{{Role: "system", Content: "你是 ContextLoom 助手，请完成任务。"}}
	for i := 0; i < 5; i++ {
		msgs = append(msgs, types.Message{Role: "user", Content: "帮我看看这段代码的问题（第" + string(rune('0'+i)) + "轮）"})
		msgs = append(msgs, types.Message{Role: "assistant", Content: "好的，我来分析。", ReasoningContent: think})
	}
	return msgs
}

// cloneMsgs 浅拷贝消息切片：各子场景独立底层数组，避免压缩步骤的原地移动互相影响。
func cloneMsgs(msgs []types.Message) []types.Message {
	out := make([]types.Message, len(msgs))
	copy(out, msgs)
	return out
}

// newBudgetAtRatio 按"压缩前占用恰好为 ratio"反推预算（整数运算确定性；场景自校验）。
func newBudgetAtRatio(t *testing.T, msgs []types.Message, ratio float64) *TokenBudget {
	t.Helper()
	used := MessagesTokens(msgs)
	if used <= 0 {
		t.Fatalf("场景消息 token 估算为 0，无法构造预算")
	}
	b := NewTokenBudget(int(float64(used) / ratio))
	if u := b.Usage(used); u <= b.SafetyRatio || u >= 1 {
		t.Fatalf("场景应落在 (安全红线 %.2f, 100%%) 区间，实际 %.4f", b.SafetyRatio, u)
	}
	return b
}

// TestThinkingProtect_VerifyRound 覆盖设计文档 §6.1 #11：
//  1. 保护后验证轮思考在压缩后存活（对照组未打标 → 同场景被削，证明标记生效）；
//  2. 零值策略压缩结果与现状逐字段一致（MarkProtected 恒等返回）；
//  3. 摘要 ≤400 runes 带来源标记（ThinkingSummary 尾段提取）。
func TestThinkingProtect_VerifyRound(t *testing.T) {
	t.Run("ProtectedThinking_SurvivesCompaction", testProtectedThinkingSurvives)
	t.Run("ZeroValue_Identity_And_CompactionEquivalence", testZeroValueEquivalence)
	t.Run("ThinkingSummary_TailCappedWithMarker", testThinkingSummaryTail)
}

func testProtectedThinkingSurvives(t *testing.T) {
	base := buildVerifyRoundMsgs()
	bigThink := base[4].ReasoningContent
	medThink := base[2].ReasoningContent

	// ── 实验组①：ProtectVerifyRound 标记"上一轮"（assistant[4]）思考 ──
	marked := MarkProtected(cloneMsgs(base), ThinkingRetention{ProtectVerifyRound: true})
	if !marked[4].ReasoningProtected {
		t.Fatalf("ProtectVerifyRound 应标记上一轮 assistant[4] 的思考")
	}
	if marked[2].ReasoningProtected {
		t.Fatalf("ProtectVerifyRound 只应标记 1 轮，assistant[2] 不应被标记")
	}
	b := newBudgetAtRatio(t, marked, 0.95)
	used := MessagesTokens(marked)
	out, changed := CompactToBudgetWithEvict(marked, b, nil)
	if !changed {
		t.Fatalf("占用 %.4f 应触发压缩，实际 changed=false", b.Usage(used))
	}
	if len(out) != len(base) {
		t.Fatalf("消息数不应变化：%d→%d", len(base), len(out))
	}
	baseUser1, _ := base[1].Content.(string)
	outUser1, ok := out[1].Content.(string)
	if !ok {
		t.Fatalf("msg[1] 正文类型异常：%T", out[1].Content)
	}
	t.Logf("实验组①压缩后：msg[1] 正文 %d→%d；msg[2] 思考 %d→%d；msg[4] 思考 %d→%d 字符",
		len([]rune(baseUser1)), len([]rune(outUser1)),
		len([]rune(medThink)), len([]rune(out[2].ReasoningContent)),
		len([]rune(bigThink)), len([]rune(out[4].ReasoningContent)))
	// ① 验证轮思考存活：完整保留（step3 二次截断与 step4 折半都不得触碰）
	if out[4].ReasoningContent != bigThink {
		t.Fatalf("验证轮思考未存活：压缩后 %d 字符，期望完整 %d 字符（保护标记未生效）",
			len([]rune(out[4].ReasoningContent)), len([]rune(bigThink)))
	}
	// ② 压缩确实发生：旧轮思考被削（保护只针对思考；长正文同样被削）
	if out[2].ReasoningContent == medThink {
		t.Fatalf("旧轮思考应被压缩（对照），实际未被削")
	}
	if outUser1 == baseUser1 {
		t.Fatalf("长正文应被压缩（保护不冻结正文），实际未被削")
	}

	// ── 对照组：同一场景不打标 → 同一思考被削，证明存活源自保护标记 ──
	bC := newBudgetAtRatio(t, base, 0.95)
	outC, _ := CompactToBudgetWithEvict(cloneMsgs(base), bC, nil)
	if outC[4].ReasoningContent == bigThink {
		t.Fatalf("对照组（未打标）思考竟完整存活——场景未触发对思考的压缩，测试无鉴别力")
	}
	t.Logf("对照组（未打标）：msg[4] 思考被削为 %d 字符", len([]rune(outC[4].ReasoningContent)))

	// ── 实验组②：ProtectRounds=2 同时保护旧轮思考，两条思考均完整存活 ──
	marked2 := MarkProtected(cloneMsgs(base), ThinkingRetention{ProtectRounds: 2})
	if !marked2[4].ReasoningProtected || !marked2[2].ReasoningProtected {
		t.Fatalf("ProtectRounds=2 应标记最近 2 条含思考的 assistant 消息")
	}
	b2 := newBudgetAtRatio(t, marked2, 0.95)
	out2, changed2 := CompactToBudgetWithEvict(marked2, b2, nil)
	if !changed2 {
		t.Fatalf("ProtectRounds=2 场景应触发压缩（长正文可削），实际 changed=false")
	}
	if out2[4].ReasoningContent != bigThink || out2[2].ReasoningContent != medThink {
		t.Fatalf("ProtectRounds=2 下两条受保护思考都应完整存活：msg[2] %d/%d，msg[4] %d/%d",
			len([]rune(out2[2].ReasoningContent)), len([]rune(medThink)),
			len([]rune(out2[4].ReasoningContent)), len([]rune(bigThink)))
	}
	t.Logf("实验组②压缩后：msg[2] 思考 %d 字符、msg[4] 思考 %d 字符（均完整存活）",
		len([]rune(out2[2].ReasoningContent)), len([]rune(out2[4].ReasoningContent)))
}

func testZeroValueEquivalence(t *testing.T) {
	msgs := buildThinkingHeavyHistory()

	// ① 恒等返回：同一底层数组、无任何标记
	out := MarkProtected(msgs, ThinkingRetention{})
	if len(out) != len(msgs) {
		t.Fatalf("零值策略应恒等返回：len %d→%d", len(msgs), len(out))
	}
	if &out[0] != &msgs[0] {
		t.Fatalf("零值策略应恒等返回（同一底层数组），实际发生了拷贝")
	}
	for i := range out {
		if out[i].ReasoningProtected {
			t.Fatalf("零值策略不应打任何保护标记：msg[%d] 被标记", i)
		}
	}

	// ② 手动压缩路径（step1-4 全层）：零值策略压缩结果与现状逐字段一致
	baseOut, baseChanged := NewTokenBudget(24000).ForceCompact(cloneMsgs(msgs))
	markedOut, markedChanged := NewTokenBudget(24000).ForceCompact(MarkProtected(cloneMsgs(msgs), ThinkingRetention{}))
	if baseChanged != markedChanged || !reflect.DeepEqual(baseOut, markedOut) {
		t.Fatalf("零值策略手动压缩结果应与现状逐字段一致（changed %v/%v；消息数 %d/%d）",
			baseChanged, markedChanged, len(baseOut), len(markedOut))
	}

	// ③ 自动压缩路径（自然阈值分层）：同样逐字段一致
	autoOut1, ch1 := CompactToBudget(cloneMsgs(msgs), NewTokenBudget(24000))
	autoOut2, ch2 := CompactToBudget(MarkProtected(cloneMsgs(msgs), ThinkingRetention{}), NewTokenBudget(24000))
	if ch1 != ch2 || !reflect.DeepEqual(autoOut1, autoOut2) {
		t.Fatalf("零值策略自动压缩结果应与现状逐字段一致（changed %v/%v；消息数 %d/%d）",
			ch1, ch2, len(autoOut1), len(autoOut2))
	}
	t.Logf("零值策略等价：手动路径 changed=%v 消息数 %d；自动路径 changed=%v 消息数 %d",
		baseChanged, len(baseOut), ch1, len(autoOut1))
}

func testThinkingSummaryTail(t *testing.T) {
	// 无思考内容 → ""
	if got := ThinkingSummary(nil, 400); got != "" {
		t.Fatalf("空消息列表应返回空串，实际 %q", got)
	}
	if got := ThinkingSummary([]types.Message{{Role: "user", Content: "hi"}}, 400); got != "" {
		t.Fatalf("无思考内容应返回空串，实际 %q", got)
	}

	tail := "结论：采用方案 B，先改配置再动执行链。"
	reasoning := strings.Repeat("反复推演各种边界情况并权衡方案。", 100) + tail // 1600+ 字符
	msgs := []types.Message{{Role: "assistant", Content: "ok", ReasoningContent: reasoning}}
	got := ThinkingSummary(msgs, 400)
	if !strings.HasPrefix(got, ThinkingSummaryMarker) {
		t.Fatalf("摘要应带来源标记 %q，实际 %q", ThinkingSummaryMarker, got)
	}
	if n := len([]rune(got)); n > 400 {
		t.Fatalf("摘要应 ≤400 runes，实际 %d", n)
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("摘要应取思考尾段（结论在末尾），实际 %q", got)
	}
	t.Logf("摘要长度 %d runes（上限 400），结尾保留结论尾段", len([]rune(got)))

	// 取最近一段非空思考的尾段（最后一条优先）
	msgs2 := []types.Message{
		{Role: "assistant", ReasoningContent: "旧思考：与本次结论无关。"},
		{Role: "user", Content: "..."},
		{Role: "assistant", ReasoningContent: reasoning},
	}
	if got2 := ThinkingSummary(msgs2, 400); !strings.HasSuffix(got2, tail) {
		t.Fatalf("应取最近一段非空思考的尾段，实际 %q", got2)
	}

	// 默认上限（maxRunes<=0 → 400）与短思考完整保留
	if n := len([]rune(ThinkingSummary(msgs, 0))); n > DefaultThinkingSummaryRunes {
		t.Fatalf("默认上限应 ≤%d runes，实际 %d", DefaultThinkingSummaryRunes, n)
	}
	short := "简短思考，直接返回。"
	if got3 := ThinkingSummary([]types.Message{{Role: "assistant", ReasoningContent: short}}, 400); !strings.HasSuffix(got3, short) {
		t.Fatalf("短思考应完整保留，实际 %q", got3)
	}
}
