package runtime

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestCompressContext_LongHistoryBelowRedline 用户真实场景：
// 长对话（条数 >> keepRecent）但整体用量未超安全红线时，手动 /compress
// 必须真实减少 token 量（状态栏 ctx 下降），而不是返回"截断 0 条"。
func TestCompressContext_LongHistoryBelowRedline(t *testing.T) {
	rt, _ := newTestRuntime(t)
	// 构造长历史：条数远超 keepRecent（8），整体用量未超安全红线
	var msgs []types.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs,
			types.Message{Role: "user", Content: strings.Repeat("用户消息内容", 12)},
			types.Message{Role: "assistant", Content: strings.Repeat("助手回复内容", 12)},
		)
	}
	rt.historyMu.Lock()
	rt.conversationHistory = msgs
	rt.historyMu.Unlock()

	before := ctxmgr.MessagesTokens(rt.conversationHistory)
	if rt.ctxBudget != nil && rt.ctxBudget.SafetyExceeded(before) {
		t.Fatalf("测试前提错误：历史应未超安全红线 (used=%d)", before)
	}

	msg := rt.CompressContext()
	t.Logf("CompressContext 结果: %s", msg)
	after := ctxmgr.MessagesTokens(rt.conversationHistory)
	if after >= before {
		t.Fatalf("压缩后 token 量应下降: before=%d after=%d", before, after)
	}
	if !strings.Contains(msg, "释放") {
		t.Fatalf("应报告释放 token: %s", msg)
	}
}

// TestCompressContext_NoChangeReportsNoNeed 短历史（条数 ≤ keepRecent 且未超
// 安全红线）手动压缩不产生任何动作时，应返回"无需压缩"文案（含当前占用），
// 而不是误导性的"释放 0 tokens（0.00% → 0.00%）"——GUI/TUI 用户据此知道
// 压缩按钮已生效但当前确实无需压缩，界面"有反应"。
func TestCompressContext_NoChangeReportsNoNeed(t *testing.T) {
	rt, _ := newTestRuntime(t)
	rt.historyMu.Lock()
	rt.conversationHistory = []types.Message{
		{Role: "system", Content: "任务指令"},
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！有什么可以帮你？"},
	}
	rt.historyMu.Unlock()

	msg := rt.CompressContext()
	t.Logf("CompressContext 结果: %s", msg)
	if !strings.Contains(msg, "无需压缩") {
		t.Fatalf("短历史压缩应报告无需压缩: %s", msg)
	}
	if !strings.Contains(msg, "%") {
		t.Fatalf("无需压缩文案应含当前占用百分比: %s", msg)
	}
}

// TestCompressContext_AfterCompressReportsNoNeed 已压缩过（历史已含 [历史摘要]
// 且条数少）再压缩时应报告无需压缩——"压缩一次后再点击快捷键无反应"场景，
// 现在有明确文案反馈。
func TestCompressContext_AfterCompressReportsNoNeed(t *testing.T) {
	rt, _ := newTestRuntime(t)
	rt.historyMu.Lock()
	rt.conversationHistory = []types.Message{
		{Role: "system", Content: "任务指令"},
		{Role: "user", Content: "[历史摘要] 前面对话的摘要内容"},
		{Role: "user", Content: "最新问题"},
		{Role: "assistant", Content: "最新回答"},
	}
	rt.historyMu.Unlock()

	msg := rt.CompressContext()
	t.Logf("CompressContext 结果: %s", msg)
	if !strings.Contains(msg, "无需压缩") {
		t.Fatalf("已压缩过再压缩应报告无需压缩: %s", msg)
	}
}

// TestCompressContext_ExhaustedStillReportsNoNeed 压无可压回归：历史条数远超
// keepRecent 且已多次压缩（所有消息到截断下限、无可释放 token）时，再压缩
// 必须返回"无需压缩"单文案，绝不能返回自相矛盾的
// "已压缩：截断 N/丢弃 N，释放 0 tokens（x% → x%）"——后者占比不变却宣称
// "已压缩"，正是用户"压缩一次后再点无反应/只能压缩一次"感知的后端根因。
func TestCompressContext_ExhaustedStillReportsNoNeed(t *testing.T) {
	rt, _ := newTestRuntime(t)
	var msgs []types.Message
	for i := 0; i < 20; i++ {
		msgs = append(msgs,
			types.Message{Role: "user", Content: strings.Repeat("用户消息内容", 12)},
			types.Message{Role: "assistant", Content: strings.Repeat("助手回复内容", 12)},
		)
	}
	rt.historyMu.Lock()
	rt.conversationHistory = msgs
	rt.historyMu.Unlock()

	// 连压直到压无可压（手动压缩渐进式：每轮减半，20 条约需 6~7 轮
	// 收敛到 1 条；此测试前几轮都应成功释放，最后一轮返回无需压缩）
	var last string
	for i := 0; i < 20; i++ {
		last = rt.CompressContext()
		t.Logf("[%d] %s", i+1, last)
		if strings.Contains(last, "无需压缩") {
			break
		}
	}
	if !strings.Contains(last, "无需压缩") {
		t.Fatalf("压无可压时应返回'无需压缩': %s", last)
	}
	if strings.Contains(last, "释放 0") {
		t.Fatalf("文案矛盾：不应出现'释放 0 tokens'宣称已压缩: %s", last)
	}
}
