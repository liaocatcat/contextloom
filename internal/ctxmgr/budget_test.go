package ctxmgr

import (
	"fmt"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func bigMsg(role string, n int) types.Message {
	return types.Message{Role: role, Content: strings.Repeat("长文本内容测试token估算压缩行为", n)}
}

func TestDecide_Thresholds(t *testing.T) {
	b := NewTokenBudget(1000)
	b.KeepRecent = 2

	// 条数不足 → 不动
	if b.Decide(100, 2) != ActionNone {
		t.Fatal("消息数不足时不应压缩")
	}
	// 低用量 → 不动
	if b.Decide(100, 10) != ActionNone {
		t.Fatal("低用量不应压缩")
	}
	// >70% → 轻度
	if b.Decide(750, 10) != ActionCompact {
		t.Fatal(">70% 应轻度压缩")
	}
	// >85% → 重度
	if b.Decide(900, 10) != ActionHardCompact {
		t.Fatal(">85% 应重度压缩")
	}
}

func TestCompactToBudget_KeepsRecent(t *testing.T) {
	b := NewTokenBudget(1000)
	b.KeepRecent = 3
	b.CompactRatio = 0.5
	b.HardRatio = 0.8

	var msgs []types.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, bigMsg("user", 20))
	}
	out, changed := CompactToBudget(msgs, b)
	if !changed {
		t.Fatal("应发生压缩")
	}
	// 最近 3 条必须保留（不丢弃；预算不足时允许截断，但条数不减少）
	if len(out) < b.KeepRecent {
		t.Fatalf("最近消息被丢弃: %d < %d", len(out), b.KeepRecent)
	}
	// 压缩后不超过安全红线（按估算）
	if b.Usage(MessagesTokens(out)) > b.SafetyRatio {
		t.Fatalf("压缩后仍超安全红线: %.2f", b.Usage(MessagesTokens(out)))
	}
}

func TestCompactToBudget_DropsOldestAndSummarizes(t *testing.T) {
	b := NewTokenBudget(500)
	b.KeepRecent = 2
	b.CompactRatio = 0.4
	b.HardRatio = 0.7
	b.SafetyRatio = 0.95

	var msgs []types.Message
	for i := 0; i < 12; i++ {
		msgs = append(msgs, bigMsg("user", 30))
	}
	out, _ := CompactToBudget(msgs, b)
	if len(out) < b.KeepRecent {
		t.Fatalf("丢弃过度: %d < %d", len(out), b.KeepRecent)
	}
	if len(out) >= len(msgs) {
		t.Fatal("未发生丢弃")
	}
	// 滚动摘要应记录被丢弃内容
	if b.Summary == "" {
		t.Fatal("滚动摘要应为空")
	}
	// 摘要消息可生成
	sm := b.SummaryMessage()
	if sm == nil || !strings.HasPrefix(sm.Content.(string), "[历史摘要]") {
		t.Fatal("摘要消息格式错误")
	}
	// 压缩审计应记录丢弃动作
	audit := b.AuditLog()
	if len(audit) == 0 {
		t.Fatal("压缩审计应为空")
	}
	foundDrop := false
	for _, a := range audit {
		if strings.Contains(a, "丢弃") {
			foundDrop = true
			break
		}
	}
	if !foundDrop {
		t.Errorf("审计日志缺少丢弃记录: %v", audit)
	}
}

// TestAuditLog_Reset 验证 ResetSummary 同时清空审计日志。
func TestAuditLog_Reset(t *testing.T) {
	b := NewTokenBudget(500)
	b.KeepRecent = 2
	b.CompactRatio = 0.4
	b.HardRatio = 0.7
	b.SafetyRatio = 0.95
	var msgs []types.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs, bigMsg("user", 30))
	}
	CompactToBudget(msgs, b)
	if len(b.AuditLog()) == 0 {
		t.Fatal("压缩后应有审计记录")
	}
	b.ResetSummary()
	if len(b.AuditLog()) != 0 {
		t.Fatal("ResetSummary 后审计日志应清空")
	}
	if b.Summary != "" {
		t.Fatal("ResetSummary 后滚动摘要应清空")
	}
}

func TestCompactToBudget_UnderBudgetNoop(t *testing.T) {
	b := NewTokenBudget(100000)
	var msgs []types.Message
	for i := 0; i < 5; i++ {
		msgs = append(msgs, bigMsg("user", 10))
	}
	out, changed := CompactToBudget(msgs, b)
	if changed {
		t.Fatal("预算充足不应压缩")
	}
	if len(out) != len(msgs) {
		t.Fatal("不应改动消息")
	}
}

func TestSafetyExceeded(t *testing.T) {
	b := NewTokenBudget(1000)
	if b.SafetyExceeded(800) {
		t.Fatal("80% 不应超红线")
	}
	if !b.SafetyExceeded(930) {
		t.Fatal("93% 应超红线")
	}
}

func TestApplyConfig_Monotonic(t *testing.T) {
	b := NewTokenBudget(1000)
	// 传乱序阈值，应自动修正单调性
	b.ApplyConfig(0.9, 0.5, 0.6, 8)
	if !(b.CompactRatio < b.HardRatio && b.HardRatio < b.SafetyRatio) {
		t.Fatalf("阈值未修正: %v %v %v", b.CompactRatio, b.HardRatio, b.SafetyRatio)
	}
}

func TestTruncateForBudget_Unicode(t *testing.T) {
	s := "你好世界你好世界你好世界"
	got := TruncateForBudget(s, 5)
	if !strings.HasSuffix(got, "…") {
		t.Fatal("应带省略号")
	}
	if len([]rune(got)) != 6 { // 5 字符 + …
		t.Fatalf("长度错误: %d", len([]rune(got)))
	}
}

func TestStripOrphanToolMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "call", ToolCalls: []types.ToolCall{{ID: "c1"}, {ID: "c2"}}},
		{Role: "tool", ToolCallID: "c1", Content: "r1"},
		{Role: "tool", ToolCallID: "c2", Content: "r2"},
		{Role: "tool", ToolCallID: "c999", Content: "orphan"}, // 孤儿：无对应 assistant 调用
		{Role: "assistant", Content: "final"},
	}
	out := StripOrphanToolMessages(msgs)
	if len(out) != 5 {
		t.Fatalf("应移除 1 条孤儿消息, got %d", len(out))
	}
	for _, m := range out {
		if m.Role == "tool" && m.ToolCallID == "c999" {
			t.Fatal("孤儿 tool 消息未移除")
		}
	}
	// 配对完整：c1/c2 应保留
	got := map[string]bool{}
	for _, m := range out {
		if m.Role == "tool" {
			got[m.ToolCallID] = true
		}
	}
	if !got["c1"] || !got["c2"] {
		t.Fatal("合法 tool 消息被误删")
	}
}

func TestCompactToBudget_ProtectsSummary(t *testing.T) {
	b := NewTokenBudget(1000)
	b.KeepRecent = 2
	b.CompactRatio = 0.3
	b.HardRatio = 0.6
	b.SafetyRatio = 0.92
	b.Summary = "旧内容要点"

	msgs := []types.Message{
		{Role: "system", Content: "[历史摘要] " + b.Summary}, // 摘要消息（受保护）
		bigMsg("user", 40),
		bigMsg("assistant", 40),
		bigMsg("user", 40),
		bigMsg("assistant", 40),
	}
	out, _ := CompactToBudget(msgs, b)
	if !isSummaryMsg(out[0]) {
		t.Fatalf("摘要消息应受保护保留, first=%q", out[0].Content)
	}
	if len(out) < 3 {
		t.Fatalf("保留过少: %d", len(out))
	}
}

// TestForceCompact 手动 /compress 强制压缩：未超预算也压缩，且保留最近 keepRecent 条完整。
func TestForceCompact(t *testing.T) {
	b := NewTokenBudget(10000)
	b.ApplyConfig(0.70, 0.85, 0.92, 3)
	var msgs []types.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			types.Message{Role: "user", Content: strings.Repeat(fmt.Sprintf("用户消息%d：", i), 20)},
			types.Message{Role: "assistant", Content: strings.Repeat(fmt.Sprintf("助手回复%d：", i), 20)},
		)
	}
	before := MessagesTokens(msgs)
	out, changed := b.ForceCompact(msgs)
	if !changed {
		t.Fatal("ForceCompact 应始终压缩（手动命令语义）")
	}
	after := MessagesTokens(out)
	if after >= before {
		t.Fatalf("强制压缩后应下降: before=%d after=%d", before, after)
	}
	// 手动压缩渐进式：每次压掉一半（len/2 下限 1），单次调用条数应减半左右
	if len(out) >= len(msgs) {
		t.Fatalf("单次压缩应减半: %d → %d", len(msgs), len(out))
	}
	// 连续压缩应能一直压到只剩 1 条（无 system 时）——旧版"压缩不受限"语义
	cur := out
	for i := 0; i < 20; i++ {
		next, c := b.ForceCompact(cur)
		if !c {
			break
		}
		cur = next
	}
	if len(cur) != 1 {
		t.Fatalf("连续压缩应能压到只剩 1 条（无 system 时）: got %d", len(cur))
	}
	// 审计应有记录
	if len(b.AuditLog()) == 0 {
		t.Error("强制压缩应产生审计记录")
	}
}

// TestForceCompact_SmallHistory 历史很小（1 条消息=压缩终点）时不压缩且不报错；
// 2 条消息仍可压缩到 1 条（旧版"压缩不受限、可一直压到只剩一条记录"语义）。
func TestForceCompact_SmallHistory(t *testing.T) {
	b := NewTokenBudget(10000)
	b.ApplyConfig(0.70, 0.85, 0.92, 3)
	msgs := []types.Message{
		{Role: "user", Content: "你好"},
	}
	out, changed := b.ForceCompact(msgs)
	if changed {
		t.Error("1 条消息（压缩终点）不应压缩")
	}
	if len(out) != 1 {
		t.Errorf("1 条消息应原样返回: got %d", len(out))
	}
	// 2 条消息 → 可压到 1 条
	msgs2 := []types.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！"},
	}
	out2, changed2 := b.ForceCompact(msgs2)
	if !changed2 {
		t.Error("2 条消息仍应压缩（可压到只剩 1 条）")
	}
	if len(out2) != 1 {
		t.Errorf("2 条消息应压到 1 条: got %d", len(out2))
	}
}

// TestCompactToBudgetWithEvict_Truncated 前缀截断时应回调原始完整内容（未被截断）。
func TestCompactToBudgetWithEvict_Truncated(t *testing.T) {
	b := NewTokenBudget(2000)
	b.ApplyConfig(0.30, 0.92, 0.92, 2) // 压低 compact 阈值确保截断发生
	var evicted []types.Message
	msgs := []types.Message{
		bigMsg("user", 300),      // 旧消息：将被前缀截断（完整 6000 字符）
		bigMsg("assistant", 300), // 旧消息：将被前缀截断
		{Role: "user", Content: "最近1"},
		{Role: "assistant", Content: "最近2"},
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, func(m types.Message) {
		evicted = append(evicted, m)
	})
	if !changed {
		t.Fatal("应发生压缩")
	}
	if len(evicted) != 2 {
		t.Fatalf("应恰好回调 2 条被截断的完整原文, got %d", len(evicted))
	}
	for _, m := range evicted {
		s, ok := m.Content.(string)
		if !ok {
			t.Fatalf("evict 内容应为字符串")
		}
		// 回调必须是完整原文（未被截断：6000 字符、无省略号）
		if len([]rune(s)) != 5400 || strings.HasSuffix(s, "…") {
			t.Fatalf("evict 回调应收到完整原文, got %d 字符", len([]rune(s)))
		}
	}
	// 压缩后对话中的旧消息确实被截断（证明原文确实从对话中丢失、已入索引）
	for _, m := range out[:2] {
		s, _ := m.Content.(string)
		if len([]rune(s)) >= 5400 {
			t.Fatal("旧消息应已被截断")
		}
	}
}

// TestCompactToBudgetWithEvict_Dropped 丢弃最旧时应回调完整原文，且每条原文只回调一次。
func TestCompactToBudgetWithEvict_Dropped(t *testing.T) {
	b := NewTokenBudget(1000)
	b.ApplyConfig(0.10, 0.20, 0.92, 2) // 低阈值确保触发丢弃
	var evicted []types.Message
	msgs := []types.Message{
		bigMsg("user", 150),      // 将被丢弃（完整 3000 字符）
		bigMsg("assistant", 150), // 将被丢弃
		{Role: "user", Content: "最近1"},
		{Role: "assistant", Content: "最近2"},
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, func(m types.Message) {
		evicted = append(evicted, m)
	})
	if !changed {
		t.Fatal("应发生压缩")
	}
	if len(evicted) != 2 {
		t.Fatalf("应恰好回调 2 条被丢弃的完整原文, got %d", len(evicted))
	}
	for _, m := range evicted {
		s, _ := m.Content.(string)
		// 完整原文：2700 字符、以"长文本"开头、无省略号
		if len([]rune(s)) != 2700 || !strings.HasPrefix(s, "长文本") || strings.HasSuffix(s, "…") {
			t.Fatalf("evict 应为完整原文, got %d 字符", len([]rune(s)))
		}
	}
	if len(out) != 2 {
		t.Fatalf("丢弃后应剩 keepRecent=2 条: got %d", len(out))
	}
}

// TestCompactToBudgetWithEvict_NoEvictWhenUnderBudget 未超预算时不压缩、不回调。
func TestCompactToBudgetWithEvict_NoEvictWhenUnderBudget(t *testing.T) {
	b := NewTokenBudget(100000)
	b.ApplyConfig(0.70, 0.85, 0.92, 2)
	called := 0
	msgs := []types.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！"},
		{Role: "user", Content: "继续"},
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, func(m types.Message) {
		called++
	})
	if changed || called != 0 {
		t.Fatalf("未超预算不应压缩/回调: changed=%v called=%d", changed, called)
	}
	if len(out) != 3 {
		t.Fatalf("应原样返回: got %d", len(out))
	}
}

// TestForceCompactWithEvict 手动强制压缩也应回调完整原文。
func TestForceCompactWithEvict(t *testing.T) {
	b := NewTokenBudget(10000)
	b.ApplyConfig(0.70, 0.85, 0.92, 3)
	var evicted []types.Message
	var msgs []types.Message
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			types.Message{Role: "user", Content: strings.Repeat(fmt.Sprintf("用户消息%d：", i), 20)},
			types.Message{Role: "assistant", Content: strings.Repeat(fmt.Sprintf("助手回复%d：", i), 20)},
		)
	}
	out, changed := b.ForceCompactWithEvict(msgs, func(m types.Message) {
		evicted = append(evicted, m)
	})
	if !changed {
		t.Fatal("ForceCompact 应始终压缩")
	}
	if len(evicted) == 0 {
		t.Fatal("强制压缩应回调被移除的完整原文")
	}
	for _, m := range evicted {
		s, _ := m.Content.(string)
		if s == "" || strings.HasSuffix(s, "…") {
			t.Fatalf("evict 应为非空完整原文: %q", s[:20])
		}
	}
	if len(out) >= len(msgs) {
		t.Fatalf("单次压缩应减半: %d → %d", len(msgs), len(out))
	}
	// 连续压缩应能压到只剩 1 条（无 system 时）
	cur := out
	for i := 0; i < 20; i++ {
		next, c := b.ForceCompactWithEvict(cur, func(m types.Message) {
			evicted = append(evicted, m)
		})
		if !c {
			break
		}
		cur = next
	}
	if len(cur) != 1 {
		t.Fatalf("连续压缩应能压到只剩 1 条（无 system 时）: got %d", len(cur))
	}
}

func TestMessageTokens_CountsReasoningContent(t *testing.T) {
	body := "好的，已完成修改"
	reasoning := strings.Repeat("让我思考一下这个问题的本质与解决方案", 200)
	m := types.Message{Role: "assistant", Content: body, ReasoningContent: reasoning}

	got := MessageTokens(m)
	bodyOnly := MessageTokens(types.Message{Role: "assistant", Content: body})
	if got <= bodyOnly {
		t.Fatalf("含思维链的消息 tokens 应显著大于纯正文: got=%d bodyOnly=%d", got, bodyOnly)
	}

	// 纯思维链轮（正文为空）：早退条件漏算时恒为 0 —— 右下角 ctx 不变的直接根因
	pure := MessageTokens(types.Message{Role: "assistant", ReasoningContent: reasoning})
	if pure == 0 {
		t.Fatal("纯思维链消息不应估算为 0")
	}
	if pure <= len([]rune(reasoning))/4 {
		t.Fatalf("纯思维链估算异常偏小: %d", pure)
	}
}
