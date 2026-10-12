package ctxmgr

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestForceCompact_ThinkingChainReleasesTokens 复现"76.89% 高占用却压不动"真实场景：
// thinking 模型对话，占用大头是 assistant 消息的思维链（ReasoningContent），
// 正文短（几百字符）、思维链长（数千字符）。旧实现的压缩器（step1/3/4）只截
// 正文不截思维链 → 压缩后 MessagesTokens 几乎不变 → released==0 → 返回
// "无需压缩：已压至精简态（无可再释放）"，而状态栏仍是 76.89%。
// 修复后：思维链纳入压缩（step1 前缀/step3 二次/step4 折半），占用必须显著下降。
func TestForceCompact_ThinkingChainReleasesTokens(t *testing.T) {
	// 预算 24000，构造约 76% 占用（与用户 76.89%/204000 同比例）：
	// 5 条 assistant 消息，每条正文 200 字符 + 思维链 5000 字符（中文 ~0.9 token/字符）
	maxTokens := 24000
	b := NewTokenBudget(maxTokens)

	var msgs []types.Message
	// 第 0 条 system（受保护，含任务指令）
	msgs = append(msgs, types.Message{Role: "system", Content: "你是 ContextLoom 助手，请完成任务。"})
	// 5 轮 thinking 对话：user 短 + assistant（短正文 + 长思维链）
	reasoning := strings.Repeat("我在深入思考这个问题，分析各种可能性和权衡利弊，并考虑不同的实现方案与边界情况。", 140) // ~8300 字符
	for i := 0; i < 5; i++ {
		msgs = append(msgs, types.Message{Role: "user", Content: "帮我看看这段代码的问题（第" + string(rune('0'+i)) + "轮）"})
		msgs = append(msgs, types.Message{Role: "assistant", Content: "好的，我来分析。", ReasoningContent: reasoning})
	}

	used := MessagesTokens(msgs)
	ratio := b.Usage(used)
	t.Logf("压缩前: %d tokens（%.2f%%） 消息 %d 条", used, ratio*100, len(msgs))
	if ratio < 0.70 {
		t.Fatalf("测试场景应超过压缩阈值 70%%，实际 %.2f%%——请调大思维链长度", ratio*100)
	}

	out, changed := b.ForceCompact(msgs)
	if !changed {
		t.Fatalf("ForceCompact 应产生压缩动作（占用 %.2f%% 超阈值），却返回 changed=false", ratio*100)
	}
	after := MessagesTokens(out)
	released := used - after
	t.Logf("压缩后: %d tokens（%.2f%%） 消息 %d 条 释放 %d tokens", after, b.Usage(after)*100, len(out), released)
	if released <= 0 {
		t.Fatalf("思维链应被压缩释放 token，实际 released=%d——压缩器对 ReasoningContent 仍是盲区", released)
	}
	// 至少释放 20%（真实用户场景 76.89% → 必须显著下降）
	releasedRatio := float64(released) / float64(used)
	if releasedRatio < 0.20 {
		t.Fatalf("释放比例过低: %.1f%%（期望 ≥20%%）——思维链压缩不彻底", releasedRatio*100)
	}
	if len(out) < 2 {
		t.Fatalf("压缩不应把 system+最近消息全丢光，剩余 %d 条", len(out))
	}
}
