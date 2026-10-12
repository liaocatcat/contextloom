package ctxmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 回归 7：tool_calls arguments 必须可压缩（只算不压 → 压缩后仍超红线 → 强制收尾）──
// 根因：MessageTokens 计入 ToolCalls arguments（write_file 大参数），但旧实现
// step1/3/4 只截 Content——tool_calls 消息 Content 为空 → 大参数永远压不掉 →
// agent.go 压缩后二次检查 SafetyExceeded 命中 → 工具循环任务被强制收尾。
// 修复：step4 在 content 到下限后折半 arguments（值级截断保持 JSON 合法）。
func TestCompact_TruncatesToolCallArgs(t *testing.T) {
	b := NewTokenBudget(8000)
	bigArgs := `{"path":"a.go","content":"` + strings.Repeat("包内容", 20000) + `"}` // 6 万字符
	msgs := []types.Message{
		{Role: "system", Content: "任务指令"},
		{Role: "assistant", Content: "", ToolCalls: []types.ToolCall{
			{ID: "c1", Type: "function", Function: types.FunctionCall{Name: "write_file", Arguments: bigArgs}},
		}},
		{Role: "tool", ToolCallID: "c1", Content: "写入成功"},
		{Role: "user", Content: "继续"},
		{Role: "assistant", Content: "好的"},
	}
	if !b.SafetyExceeded(MessagesTokens(msgs)) {
		t.Fatal("测试数据应超红线")
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if !changed {
		t.Fatal("tool_calls 大参数应被压缩")
	}
	if b.SafetyExceeded(MessagesTokens(out)) {
		t.Fatalf("压缩后仍超红线: %d", MessagesTokens(out))
	}
	// arguments 截断后必须是合法 JSON（API 400 防护）
	var found bool
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			if tc.ID == "c1" {
				found = true
				if tc.Function.Name != "write_file" {
					t.Fatalf("tool name 被改动: %q", tc.Function.Name)
				}
				var obj map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &obj); err != nil {
					t.Fatalf("截断后 arguments 非法 JSON: %v", err)
				}
				if len([]rune(tc.Function.Arguments)) > len([]rune(bigArgs))/5 {
					t.Fatalf("arguments 未有效压缩: %d → %d 字符", len([]rune(bigArgs)), len([]rune(tc.Function.Arguments)))
				}
			}
		}
	}
	if !found {
		t.Fatal("c1 的 tool_calls 丢失")
	}
	// 配对必须完整（c1 有 tool 响应）
	pending := map[string]bool{}
	for _, m := range out {
		for _, tc := range m.ToolCalls {
			pending[tc.ID] = true
		}
		if m.Role == "tool" {
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		t.Fatalf("压缩后存在未兑现 tool_call → API 400 风险: %v", pending)
	}
}

// ── 回归 8：strip 清理孤儿消息后 changed 必须置位（调用方 changed && 才采纳结果）──
func TestCompact_StripOrphansSetsChanged(t *testing.T) {
	b := NewTokenBudget(1000)
	b.CompactRatio = 0.05 // 极低阈值确保进入压缩路径
	msgs := []types.Message{
		{Role: "assistant", Content: strings.Repeat("a", 500), ToolCalls: []types.ToolCall{{ID: "c1"}}},
		{Role: "tool", ToolCallID: "c1", Content: strings.Repeat("r", 500)},
		{Role: "tool", ToolCallID: "c999", Content: strings.Repeat("x", 500)}, // 孤儿（无对应 assistant 调用）
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if !changed {
		t.Fatal("应发生压缩且 changed=true")
	}
	for _, m := range out {
		if m.Role == "tool" && m.ToolCallID == "c999" {
			t.Fatal("孤儿 tool 消息未被清理")
		}
	}
}

// ── 回归 9：step4 下限——大量 ≤200 字消息仍超限时必须继续折半（旧实现 200 下限 break）──
// 场景：keepRecent=40、50 条 200 字消息 → step1/2/3 后仍 125% 超限。
// 旧实现 step4 对 200 字消息判 maxLen<=200 直接 break → 压缩后仍超红线 → 强制收尾。
// 修复：非 system 下限降到 100，继续折半直至红线内。
func TestCompact_Step4_ContinuesBelow200Floor(t *testing.T) {
	b := NewTokenBudget(6000)
	b.KeepRecent = 40
	var msgs []types.Message
	for i := 0; i < 50; i++ {
		msgs = append(msgs, types.Message{Role: "user", Content: strings.Repeat("中", 200)})
	}
	if !b.SafetyExceeded(MessagesTokens(msgs)) {
		t.Fatal("测试数据应超红线")
	}
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if !changed {
		t.Fatal("应发生压缩")
	}
	if b.SafetyExceeded(MessagesTokens(out)) {
		t.Fatalf("压缩后仍超安全红线: %d tokens", MessagesTokens(out))
	}
	if len(out) < b.KeepRecent {
		t.Fatalf("keepRecent 保护失效: %d < %d", len(out), b.KeepRecent)
	}
}

// ── 回归 10：ForceCompact 小历史巨消息必须生效（旧实现 len<=keepRecent 直接短路）──
func TestForceCompact_HugeMessageSmallHistory(t *testing.T) {
	b := NewTokenBudget(4000)
	b.ApplyConfig(0.70, 0.85, 0.92, 3)
	msgs := []types.Message{
		{Role: "user", Content: strings.Repeat("大文件内容", 600)},
		{Role: "assistant", Content: strings.Repeat("回复内容", 400)},
	}
	// 2 条 ≤ keepRecent=3，但总 token 超红线 → 手动 /compress 必须生效
	if !b.SafetyExceeded(MessagesTokens(msgs)) {
		t.Fatal("测试数据应超红线")
	}
	out, changed := b.ForceCompact(msgs)
	if !changed {
		t.Fatal("小历史但超红线时 ForceCompact 应压缩（旧实现短路）")
	}
	if MessagesTokens(out) >= MessagesTokens(msgs) {
		t.Fatal("压缩无效")
	}
	// 手动压缩可压到只剩 1 条完整消息（无 system 时；旧版"压缩不受限"语义）
	if len(out) != 1 {
		t.Fatalf("手动压缩应可压到只剩 1 条: got %d", len(out))
	}
}

// ── 回归 11：MessageTokens 结构开销整条消息只计一次（旧实现每字符串 +8）──
func TestMessageTokens_SingleOverhead(t *testing.T) {
	m := types.Message{
		Role:       "assistant",
		Content:    "你好",
		Name:       "n",
		ToolCallID: "t1",
		ToolCalls: []types.ToolCall{
			{ID: "c1", Function: types.FunctionCall{Name: "read", Arguments: `{"p":"a"}`}},
			{ID: "c2", Function: types.FunctionCall{Name: "write_file", Arguments: `{"p":"b","c":"` + strings.Repeat("x", 100) + `"}`}},
		},
	}
	want := 8 + // 消息级结构开销（只一次）
		harness.TextTokens("你好") +
		harness.TextTokens("n") +
		harness.TextTokens("t1") +
		harness.TextTokens("read") + harness.TextTokens(`{"p":"a"}`) + 6 +
		harness.TextTokens("write_file") + harness.TextTokens(`{"p":"b","c":"`+strings.Repeat("x", 100)+`"}`) + 6
	if got := MessageTokens(m); got != want {
		t.Fatalf("结构开销重复计入: got=%d want=%d", got, want)
	}
	// 旧实现（每字符串 +8）会得到 6 个字符串 × 8 = 48 结构开销，远大于 8+12
	legacy := want + 5*8 // 除第一条外的每个字符串各多算一次 +8
	if got := MessageTokens(m); got >= legacy {
		t.Fatalf("结构开销疑似按字符串重复计入: got=%d legacy=%d", got, legacy)
	}
}

// ── 回归 12：truncateArgumentsJSON 保 JSON 合法 ──
func TestTruncateArgumentsJSON(t *testing.T) {
	// 1. 未超限：原样返回
	args := `{"path":"a.go","content":"ok"}`
	if got := truncateArgumentsJSON(args, 1000); got != args {
		t.Fatalf("未超限不应改动: %q", got)
	}
	// 2. 超限：截断最长字符串值，整体仍是合法 JSON 且 ≤ 预算
	big := `{"path":"a.go","content":"` + strings.Repeat("内容", 5000) + `","mode":"w"}`
	out := truncateArgumentsJSON(big, 500)
	var obj map[string]any
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("截断后非法 JSON: %v", err)
	}
	if len([]rune(out)) > 500 {
		t.Fatalf("截断后超预算: %d", len([]rune(out)))
	}
	if obj["path"] != "a.go" || obj["mode"] != "w" {
		t.Fatalf("短字段被误伤: %v", obj)
	}
	// 3. 预算极小：整体替换为最小合法 JSON（仍可解析）
	tiny := truncateArgumentsJSON(big, 10)
	if err := json.Unmarshal([]byte(tiny), &obj); err != nil {
		t.Fatalf("极小预算兜底仍应合法 JSON: %v", err)
	}
	// 4. 非 JSON 文本：兜底合法 JSON（不 400）
	weird := truncateArgumentsJSON("不是JSON的纯文本内容内容内容内容内容", 10)
	if err := json.Unmarshal([]byte(weird), &obj); err != nil {
		t.Fatalf("非法输入兜底应返回合法 JSON: %v", err)
	}
}
