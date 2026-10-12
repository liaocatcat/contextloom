package runtime

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestForceCompact_ManyHugeMsgsWithinKeepRecent 回归：手动压缩对"条数 ≤
// keepRecent 但单条消息巨大"的历史必须生效。
//
// 用户真实反馈："无需压缩：上下文已压至精简态（无可再释放），当前占用
// 76.89%（156855 tokens/204000）"。根因：ForceCompact 短路条件用
// SafetyExceeded(92%) 做判据——历史恰好 ≤ keepRecent(8) 条巨型消息（单条
// 可达 2 万 token）且占用 60~92% 时，短路把截断（step1/3/4，只削长度不丢
// 消息）一起禁掉，released==0 误报"压无可压"。
//
// 修复后：短路判据收紧为 ExceedsCompactRatio(70%)——76.89% 占用必然进入
// 压缩流程，截断巨型消息释放空间（条数不变，keepRecent"不丢弃"语义完好）。
func TestForceCompact_ManyHugeMsgsWithinKeepRecent(t *testing.T) {
	rt, _ := newTestRuntime(t)
	rt.ctxBudget = ctxmgr.NewTokenBudget(204000)
	rt.ctxBudget.ApplyConfig(0.7, 0.85, 0.92, 8)

	// 8 条巨型消息（≤ keepRecent），每条 ~1.6 万 token，总量 ~70~80%
	var msgs []types.Message
	for i := 0; i < 8; i++ {
		content := "第" + string(rune('A'+i)) + "条：" + strings.Repeat("长内容占满预算的巨型消息。", 1600)
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs = append(msgs, types.Message{Role: role, Content: content})
	}
	rt.historyMu.Lock()
	rt.conversationHistory = msgs
	rt.historyMu.Unlock()

	before := ctxmgr.MessagesTokens(rt.conversationHistory)
	pct := float64(before) / 204000 * 100
	if pct < 65 || pct > 90 {
		t.Fatalf("测试前提：占用应在用户量级 65~90%%，当前 %.2f%%", pct)
	}

	msg := rt.CompressContext()
	after := ctxmgr.MessagesTokens(rt.conversationHistory)
	t.Logf("占用 %.2f%% → 压缩 => %s", pct, msg)

	// 修复断言：高占用 + 条数在保留窗口内也必须真实释放
	if after >= before {
		t.Fatalf("76.89%% 量级占用必须释放：before=%d after=%d (%s)", before, after, msg)
	}
	if strings.Contains(msg, "无需压缩") {
		t.Fatalf("高占用不应报'无需压缩': %s", msg)
	}
	// 手动压缩不受条数限制（旧版"可压到只剩一条"语义）：8 条巨型消息
	// 场景压缩后允许丢弃到剩 1 条（system 受保护则剩 2 条），但必须释放大量 token。
	_ = len(msgs)
}

// TestForceCompact_SmallHistoryStillShortcircuits 防护：历史确实短小（占用
// 低于压缩阈值）时手动压缩仍应短路返回"无需压缩"——防止修复过度。
func TestForceCompact_SmallHistoryStillShortcircuits(t *testing.T) {
	rt, _ := newTestRuntime(t)
	rt.ctxBudget = ctxmgr.NewTokenBudget(204000)
	rt.ctxBudget.ApplyConfig(0.7, 0.85, 0.92, 8)

	rt.historyMu.Lock()
	rt.conversationHistory = []types.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！有什么可以帮你？"},
	}
	rt.historyMu.Unlock()

	msg := rt.CompressContext()
	if !strings.Contains(msg, "无需压缩") {
		t.Fatalf("短小历史应继续短路返回'无需压缩': %s", msg)
	}
}
