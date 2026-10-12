package ctxmgr

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestForceCompact_ManualUnlimitedUntilOne 手动压缩语义回归：
// 旧版行为是"压缩不受限、可以一直压到只剩一条记录"，被 keepRecent 锁死后
// 第一次压缩剩 8 条、第二次点击就"无需压缩"（用户实锤回归）。
// 修复后：ForceCompact 临时把 KeepRecent 降到 1（system 消息仍受保护恒保留），
// 连续手动压缩必须能一直压到只剩 system + 1 条完整消息，且每次压缩都有动作。
func TestForceCompact_ManualUnlimitedUntilOne(t *testing.T) {
	b := NewTokenBudget(30000)
	b.ApplyConfig(0.70, 0.85, 0.92, 8)

	// 20 轮对话 + system：压缩前条数远超 keepRecent(8)
	var msgs []types.Message
	msgs = append(msgs, types.Message{Role: "system", Content: "任务指令"})
	for i := 0; i < 20; i++ {
		msgs = append(msgs,
			types.Message{Role: "user", Content: strings.Repeat("用户消息内容", 30)},
			types.Message{Role: "assistant", Content: strings.Repeat("助手回复内容", 30)},
		)
	}

	// 连压多次直到压无可压
	rounds := 0
	prevTokens := MessagesTokens(msgs)
	for {
		out, changed := b.ForceCompact(msgs)
		rounds++
		if !changed {
			break // 压无可压
		}
		cur := MessagesTokens(out)
		if cur > prevTokens {
			t.Fatalf("压缩后 token 不应上升: %d → %d", prevTokens, cur)
		}
		prevTokens = cur
		msgs = out
		if rounds > 30 {
			t.Fatal("压缩超过 30 轮未收敛——可能死循环")
		}
	}

	// 终态：system（受保护）+ 至多 1 条完整消息（旧版"只剩一条记录"语义）
	if len(msgs) > 2 {
		t.Fatalf("手动压缩应能压到只剩 system+1 条，实际 %d 条: %v", len(msgs), msgs)
	}
	if len(msgs) < 1 {
		t.Fatal("system 消息应受保护恒保留，不应全丢")
	}
	if msgs[0].Role != "system" {
		t.Fatalf("第一条应是受保护的 system 消息，got %q", msgs[0].Role)
	}
	t.Logf("连压 %d 轮收敛：剩余 %d 条（system 受保护 + 最近 1 条）", rounds, len(msgs))

	// 自动压缩路径不受影响：KeepRecent 与 HardRatio 已恢复（defer 恢复），
	// 下次自动压缩仍保护最近 8 条、按原硬阈值丢弃
	if b.KeepRecent != 8 {
		t.Fatalf("ForceCompact 应恢复 KeepRecent=8，实际 %d", b.KeepRecent)
	}
	if b.HardRatio != 0.85 {
		t.Fatalf("ForceCompact 应恢复 HardRatio=0.85，实际 %.2f", b.HardRatio)
	}
}

// TestForceCompact_KeepRecentRestoredAfterPanic 防御：即使压缩中途 panic，
// defer 也必须恢复 KeepRecent——不能把"手动压到 1 条"的临时语义泄漏到自动压缩路径。
func TestForceCompact_KeepRecentRestoredAfterPanic(t *testing.T) {
	b := NewTokenBudget(30000)
	b.ApplyConfig(0.70, 0.85, 0.92, 8)
	msgs := []types.Message{
		{Role: "system", Content: "任务指令"},
		{Role: "user", Content: strings.Repeat("内容", 500)},
	}

	func() {
		defer func() { _ = recover() }()
		// 直接调用内部函数（模拟压缩中途 panic 的场景），验证 defer 恢复
		b.MaxTokens = 1 // 触发 step2/3/4 路径
		_, _ = b.ForceCompactWithEvict(msgs, nil)
	}()

	if b.KeepRecent != 8 {
		t.Fatalf("panic 后 KeepRecent 应恢复为 8，实际 %d", b.KeepRecent)
	}
	if b.HardRatio != 0.85 {
		t.Fatalf("panic 后 HardRatio 应恢复为 0.85，实际 %.2f", b.HardRatio)
	}
}
