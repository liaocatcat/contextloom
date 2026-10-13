package runtime

// M10 保护消费点 runtime 集成测试（§6.1 #11）：
//   1. 验证阶段（strict+verify_pass=on 未验证）→ compactHistoryWithThinking 自动
//      MarkProtected：受保护思考完整存活 + 思考尾段摘要注入消息尾部；
//   2. policy=off → 压缩结果与直接调用 CompactToBudgetWithEvict 逐字段一致（零回归）；
//   3. 重复压缩不重复注入摘要（幂等）。

import (
	"reflect"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// protectRuntimeMsgs 构造验证轮场景（与 ctxmgr thinking_protect_test 同构）：
// [0]system [1]user（长正文）[2]assistant（超长思考=保护目标）[3]user（验证轮提示）。
func protectRuntimeMsgs() []types.Message {
	bigThink := strings.Repeat("我在深入分析这个问题的各种可能实现方案与边界条件，并权衡利弊得失。", 340)
	return []types.Message{
		{Role: "system", Content: "你是 ContextLoom 助手。"},
		{Role: "user", Content: strings.Repeat("执行阶段：请实现需求，先完成核心路径。", 20)},
		{Role: "assistant", Content: "已完成关键路径实现，等待验证。", ReasoningContent: bigThink},
		{Role: "user", Content: "【验证轮】请复核上一轮产出是否正确。"},
	}
}

// cloneMsgsForRuntime 浅拷贝消息切片（各子场景独立底层数组，防压缩步骤原地移动互相影响）。
func cloneMsgsForRuntime(msgs []types.Message) []types.Message {
	out := make([]types.Message, len(msgs))
	copy(out, msgs)
	return out
}

// budgetAtRatio 按"压缩前占用恰好为 ratio"反推预算（与 ctxmgr 测试同构）。
func budgetAtRatio(t *testing.T, msgs []types.Message, ratio float64) *ctxmgr.TokenBudget {
	t.Helper()
	used := ctxmgr.MessagesTokens(msgs)
	if used <= 0 {
		t.Fatalf("场景消息 token 估算为 0，无法构造预算")
	}
	b := ctxmgr.NewTokenBudget(int(float64(used) / ratio))
	if u := b.Usage(used); u <= b.SafetyRatio || u >= 1 {
		t.Fatalf("场景应落在 (安全红线 %.2f, 100%%) 区间，实际 %.4f", b.SafetyRatio, u)
	}
	return b
}

// assistantReasoning 取第一条含思考的 assistant 消息的思考内容。
func assistantReasoning(msgs []types.Message) string {
	for _, m := range msgs {
		if m.Role == "assistant" && m.ReasoningContent != "" {
			return m.ReasoningContent
		}
	}
	return ""
}

// countThinkingSummaries 统计思考摘要注入条数（按来源标记前缀）。
func countThinkingSummaries(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok && strings.HasPrefix(s, ctxmgr.ThinkingSummaryMarker) {
			n++
		}
	}
	return n
}

// TestThinkingProtect_RuntimeIntegration：M10 消费点（runtime 压缩调用链）。
func TestThinkingProtect_RuntimeIntegration(t *testing.T) {
	base := protectRuntimeMsgs()
	bigThink := base[2].ReasoningContent

	// ── ① 验证阶段：保护生效（思考不截断 + 摘要注入）──
	rt := &Runtime{Config: &config.Config{}}
	rt.Config.Thinking.Policy = "strict"
	rt.Config.Thinking.VerifyPass = "on"
	if tr := rt.buildThinkTracker(complexTaskText(), "task", 0, ""); tr == nil {
		t.Fatal("strict+complex 应构造 Tracker")
	}
	if st := rt.ThinkingStatus(); !st.Verifying {
		t.Fatalf("strict+verify_pass=on 未验证时应处于验证阶段，实际 %+v", st)
	}
	rt.ctxBudget = budgetAtRatio(t, base, 0.95)

	out, changed := rt.compactHistoryWithThinking(cloneMsgsForRuntime(base), "sid")
	if !changed {
		t.Fatal("占用 95% 应触发压缩")
	}
	if got := assistantReasoning(out); got != bigThink {
		t.Fatalf("验证阶段受保护思考应完整存活：%d/%d 字符", len([]rune(got)), len([]rune(bigThink)))
	}
	last := out[len(out)-1]
	lc, ok := last.Content.(string)
	if !ok || !strings.HasPrefix(lc, ctxmgr.ThinkingSummaryMarker) {
		t.Fatalf("压缩后应在消息尾部注入思考摘要（带 %q 标记），实际 %v", ctxmgr.ThinkingSummaryMarker, last.Content)
	}
	if n := len([]rune(lc)); n > ctxmgr.DefaultThinkingSummaryRunes {
		t.Fatalf("摘要应 ≤%d runes，实际 %d", ctxmgr.DefaultThinkingSummaryRunes, n)
	}
	if !strings.Contains(lc, "权衡利弊得失") {
		t.Fatalf("摘要应取思考尾段（结论在末尾），实际 %q", lc)
	}

	// ── ② policy=off：与现状逐字段一致（零回归）──
	rt2 := &Runtime{Config: &config.Config{}} // 零值配置：policy=off
	if tr := rt2.buildThinkTracker(complexTaskText(), "task", 0, ""); tr != nil {
		t.Fatal("policy=off 应返回 nil（并清空快照）")
	}
	if rt2.ThinkingStatus().Verifying {
		t.Fatal("policy=off 不应处于验证阶段")
	}
	rt2.ctxBudget = budgetAtRatio(t, base, 0.95)
	got2, ch2 := rt2.compactHistoryWithThinking(cloneMsgsForRuntime(base), "sid")
	want2, chWant := ctxmgr.CompactToBudgetWithEvict(cloneMsgsForRuntime(base), budgetAtRatio(t, base, 0.95), nil)
	if ch2 != chWant || !reflect.DeepEqual(got2, want2) {
		t.Fatalf("policy=off 压缩结果应与现状逐字段一致（changed %v/%v，消息数 %d/%d）", ch2, chWant, len(got2), len(want2))
	}
	if n := countThinkingSummaries(want2); n != 0 {
		t.Fatalf("现状路径不应注入思考摘要，实际 %d 条", n)
	}
	if got := assistantReasoning(want2); got == bigThink {
		t.Fatal("对照组（未保护）思考竟完整存活——场景无鉴别力")
	} else {
		t.Logf("对照组（未保护）：思考被削为 %d 字符；保护路径完整保留 %d 字符", len([]rune(got)), len([]rune(bigThink)))
	}

	// ── ③ 重复压缩不重复注入摘要（幂等）──
	rt.ctxBudget = budgetAtRatio(t, out, 0.95)
	out3, _ := rt.compactHistoryWithThinking(out, "sid")
	if n := countThinkingSummaries(out3); n != 1 {
		t.Fatalf("思考摘要应保持 1 条（防重复注入），实际 %d", n)
	}
}
