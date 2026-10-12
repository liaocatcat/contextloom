package ctxmgr

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 回归 1：system 消息（消息列表第 0 条）在压缩时不可被截断/丢弃 ──
// 根因：旧实现 step1 前缀截断 / step2 丢弃最旧 / step3 二次截断对 role=system
// 一视同仁——agent 模式下 system 含任务指令/策略/协议，被截到 ≤400 字甚至
// 整个丢弃，模型丢失任务目标。
func TestCompact_ProtectsSystemMessage(t *testing.T) {
	b := NewTokenBudget(10000)
	sys := strings.Repeat("系统指令重要内容", 200) // 1600 字 ≈ 1920 tokens
	msgs := []types.Message{{Role: "system", Content: sys}}
	for i := 0; i < 29; i++ {
		msgs = append(msgs, types.Message{Role: "user", Content: strings.Repeat("历史内容", 125)})
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if !changed {
		t.Fatal("应发生压缩")
	}
	if len(out) == 0 || out[0].Role != "system" {
		t.Fatalf("system 消息被丢弃！out[0]=%+v", out[0])
	}
	first, _ := out[0].Content.(string)
	if first != sys {
		t.Fatalf("system 消息被截断（长度 %d → %d）", len([]rune(sys)), len([]rune(first)))
	}
}

// ── 回归 2：tool 消息截断后配对必须完整（assistant tool_calls ↔ tool 响应）──
// 根因：旧实现截断重建消息只保留 Role/Content，ToolCallID 丢失 → tool 消息被
// 当孤儿剥离 → assistant tool_calls 悬空（无 tool 响应）→ OpenAI 兼容 API 400。
func TestCompact_PreservesToolPairing(t *testing.T) {
	b := NewTokenBudget(10000)
	var msgs []types.Message
	msgs = append(msgs, types.Message{Role: "system", Content: "s"})
	for i := 0; i < 6; i++ {
		msgs = append(msgs,
			types.Message{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{{ID: "c" + string(rune('0'+i))}}},
			types.Message{Role: "tool", ToolCallID: "c" + string(rune('0'+i)), Content: strings.Repeat("很长很长的工具结果内容", 364)}, // 4004 字
		)
	}
	msgs = append(msgs,
		types.Message{Role: "user", Content: strings.Repeat("u", 100)},
		types.Message{Role: "assistant", Content: strings.Repeat("a", 100)},
	)
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if !changed {
		t.Fatal("应发生压缩")
	}
	// 压缩后：每个 assistant 的 tool_call_id 必须有对应 tool 响应
	pending := map[string]bool{}
	for _, m := range out {
		if len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		}
		if m.Role == "tool" && m.ToolCallID != "" {
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		ids := make([]string, 0, len(pending))
		for id := range pending {
			ids = append(ids, id)
		}
		t.Fatalf("压缩后存在未兑现 tool_call %v → API 400 风险", ids)
	}
}

// ── 回归 3：消息条数 ≤ KeepRecent 时巨消息也必须压缩（step4 折半兜底生效）──
// 根因：旧实现 len(msgs) <= keepRecent 提前短路，单条巨消息（如大文件读取
// 结果）超红线时压缩完全不执行 → step4 折半兜底成死代码 → API 400。
func TestCompact_SmallCountHugeMessages(t *testing.T) {
	b := NewTokenBudget(10000) // keepRecent=8
	var msgs []types.Message
	for i := 0; i < 4; i++ {
		msgs = append(msgs, types.Message{Role: "user", Content: strings.Repeat("超大数据内容", 3000)})
	}
	if got := MessagesTokens(msgs); got < 10000*9/10 {
		t.Fatalf("测试数据未超红线: %d", got)
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if !changed {
		t.Fatalf("仅 %d 条消息但 token 超红线，压缩被短路", len(msgs))
	}
	if MessagesTokens(out) >= MessagesTokens(msgs) {
		t.Fatal("压缩无效")
	}
	if MessagesTokens(out) > 10000*92/100 {
		t.Fatalf("压缩后仍超安全红线: %d", MessagesTokens(out))
	}
}

// ── 回归 4：极端场景——只有 system 巨消息超限时，最后手段折半 system ──
// 保证不 panic、不无限循环，且最终回到红线内（或系统提示词被折半到下限）。
func TestCompact_SystemOnlyLastResort(t *testing.T) {
	b := NewTokenBudget(10000)
	msgs := []types.Message{
		{Role: "system", Content: strings.Repeat("唯一超大系统指令", 20000)}, // 24万字
	}
	if MessagesTokens(msgs) <= 10000*92/100 {
		t.Fatal("测试数据未超红线")
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if !changed {
		t.Fatal("应发生压缩")
	}
	if len(out) != 1 || out[0].Role != "system" {
		t.Fatalf("system 消息应保留且仅剩 1 条: %+v", out)
	}
	s, _ := out[0].Content.(string)
	// 最后手段语义：保证不超红线（不 400）或已折半到 200 字下限；
	// system 提示词由调用方构建（通常几千字），此极端场景仅防御不 panic/不死循环
	if MessagesTokens(out) > 10000*92/100 && len([]rune(s)) > 200 {
		t.Fatalf("压缩后仍超红线且未达下限: %d", MessagesTokens(out))
	}
}

// ── 回归 5：MessageTokens 必须计入 tool_calls 参数 JSON（write_file 大参数）──
func TestMessageTokens_CountsToolCallArgs(t *testing.T) {
	// 仅内容（无 tool_calls）
	plain := types.Message{Role: "assistant", Content: "好的"}
	// 带大参数 tool_call（write_file 内容可达数千 token）
	bigArgs := `{"path":"a.go","content":"` + strings.Repeat("package main", 300) + `"}`
	withCall := types.Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []types.ToolCall{
			{ID: "call_1", Type: "function", Function: types.FunctionCall{Name: "write_file", Arguments: bigArgs}},
		},
	}
	withTokens := MessageTokens(withCall)
	plainTokens := MessageTokens(plain)
	if withTokens <= plainTokens {
		t.Fatalf("tool_calls 参数未计入估算: plain=%d withCall=%d", plainTokens, withTokens)
	}
	if withTokens < harness.EstimateTokens(bigArgs) {
		t.Fatalf("tool_calls 参数估算低于其文本本身: %d < %d", withTokens, harness.EstimateTokens(bigArgs))
	}
}

// ── 回归 6：空消息不产生虚高 overhead ──
func TestMessageTokens_EmptyNoOverhead(t *testing.T) {
	if got := MessageTokens(types.Message{}); got != 0 {
		t.Fatalf("空消息估算应为 0，实际 %d", got)
	}
	if got := MessageTokens(types.Message{Role: "tool", Content: ""}); got != 0 {
		t.Fatalf("空内容消息估算应为 0，实际 %d", got)
	}
}
