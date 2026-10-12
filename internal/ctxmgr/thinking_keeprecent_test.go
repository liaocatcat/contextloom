package ctxmgr

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestForceCompact_ExactlyKeepRecentThinking 精确复现用户反馈场景：
// 历史恰好 8 条消息（≤ keepRecent）且占用 76.89%——此时 step1（前缀截断，
// 需 len>keep）与 step2（丢弃最旧，需 len>keep 且超 HardRatio）都不触发，
// 只有 step3（安全红线二次截断，需超 92% SafetyRatio）与 step4（折半兜底，
// ForceCompact 会把预算压到当前用量 60% 强制触发）可能动作。
// 旧实现：step3/step4 只折半正文（thinking 模型正文短），思维链（占用大头）
// 原样保留 → released≈0 → CompressContext 返回"无需压缩：已压至精简态
// （无可再释放）"，状态栏仍 76.89%——用户"高占用却压不动"的精确代码根因。
func TestForceCompact_ExactlyKeepRecentThinking(t *testing.T) {
	// 预算 30000：与用户 76.89%/204000 同比例场景（触发路径与预算绝对值无关，
	// 关键在"条数 ≤ keepRecent 且 70% < 占用 < 85%"）。
	maxTokens := 30000
	b := NewTokenBudget(maxTokens)

	// 构造恰好 8 条消息、占用 ~76.89%：1 system + 7 条 assistant（短正文 + 长思维链）
	reasoning := strings.Repeat("仔细分析需求、设计实现、权衡取舍并推演边界条件与失败场景。", 130) // ~3380 字符 ≈ 3000 token
	var msgs []types.Message
	msgs = append(msgs, types.Message{Role: "system", Content: "你是 ContextLoom 助手。"})
	for i := 0; i < 7; i++ {
		msgs = append(msgs, types.Message{Role: "assistant", Content: "结论：可以。", ReasoningContent: reasoning})
	}
	used := MessagesTokens(msgs)
	ratio := b.Usage(used)
	t.Logf("压缩前: %d tokens（%.2f%%） 消息 %d 条（keepRecent=%d）", used, ratio*100, len(msgs), b.KeepRecent)
	if ratio < 0.70 || ratio > 0.85 {
		t.Fatalf("测试应落在 70%%~85%% 之间（超压缩阈值、未超硬丢弃阈值），实际 %.2f%%", ratio*100)
	}
	if len(msgs) > b.KeepRecent {
		t.Fatalf("测试应 ≤ keepRecent(%d)，实际 %d 条", b.KeepRecent, len(msgs))
	}

	out, changed := b.ForceCompact(msgs)
	if !changed {
		t.Fatalf("ForceCompact 应产生动作（占用 %.2f%% 超压缩阈值），却返回 changed=false", ratio*100)
	}
	after := MessagesTokens(out)
	released := used - after
	t.Logf("压缩后: %d tokens（%.2f%%） 消息 %d 条 释放 %d tokens", after, b.Usage(after)*100, len(out), released)
	if released == 0 {
		t.Fatalf("76.89%% 占用、8 条消息场景 released==0——思维链仍未被压缩（真实用户反馈复现）")
	}
	// 释放应显著：思维链 6900×7=48300 token 大头，至少释放 30%
	releasedRatio := float64(released) / float64(used)
	if releasedRatio < 0.30 {
		t.Fatalf("释放比例过低: %.1f%%（期望 ≥30%%）——思维链压缩不彻底", releasedRatio*100)
	}
}
