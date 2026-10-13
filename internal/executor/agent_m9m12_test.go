package executor

// M9 验证子代理 + M12 止损接线 executor 层测试：
//   1. 送审触发条件：strict+complex+已验证+产出证据 → hook 1 次并回注再走一轮；
//      simple/auto/未验证/无证据/goal/复核任务/已送审 → 0 次；hook nil → 零回归；
//   2. 止损：连续无新证据停软注入、有工具证据不 stall、strict 硬拦截不受止损影响。
// 复用 agent_think_test.go 的 mock/助手（同一 package）。

import (
	"strings"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/thinkmgr"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── M9 测试替身 ──

// verifierSpyHook 记录复核调用与请求（M9 测试替身）。
type verifierSpyHook struct {
	mu    sync.Mutex
	calls int
	reqs  []VerifierRequest
}

func (h *verifierSpyHook) hook() func(req *VerifierRequest) (string, error) {
	return func(req *VerifierRequest) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls++
		h.reqs = append(h.reqs, *req)
		return "通过：证据核对无问题；关键数字复算一致。", nil
	}
}

func (h *verifierSpyHook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

// strictComplexVerifiedTracker strict+complex+VerifyPass=on（minRounds=1：
// 首轮 [THINK_VERIFIED] 即达标放行，聚焦送审路径）。
func strictComplexVerifiedTracker() *thinkmgr.Tracker {
	return strictComplexTracker(1)
}

// ── M9 用例 ──

// TestVerifier_DefaultTrigger（§6.1 #10）：strict+complex+文件产出证据 → hook 被调 1 次，
// 复核结果以【复核结果】回注并再走一轮；hook nil / 条件不满足 → 0 次且消息序列零回归。
func TestVerifier_DefaultTrigger(t *testing.T) {
	run := func(attachHook bool, evidence bool) (*verifierSpyHook, int, [][]types.Message, string) {
		t.Helper()
		mb := newThinkMock("最终答案 [THINK_VERIFIED]", "复核后的最终结论")
		agent := newThinkTestAgent(mb, "复杂任务：多步骤实现与验证")
		agent.SetThinkPolicy(strictComplexVerifiedTracker())
		agent.sawWriteEvidence = evidence // 模拟 write_file/replace_in_file 证据
		spy := &verifierSpyHook{}
		if attachHook {
			agent.SetVerifierHook(spy.hook())
		}
		out := runStreamAndDrain(t, agent)
		calls, reqs := mb.snapshot()
		return spy, calls, reqs, out
	}

	// ① 条件满足：送审 1 次 + 回注再走一轮
	spy, calls, reqs, out := run(true, true)
	if got := spy.count(); got != 1 {
		t.Fatalf("strict+complex+已验证+证据应送审 1 次，实际 %d 次", got)
	}
	if calls != 2 {
		t.Fatalf("送审后应再走一轮（共 2 次模型调用），实际 %d", calls)
	}
	if !requestsContain(reqs[1], VerifierResultPrefix) {
		t.Fatalf("第 2 次请求应包含复核结果回注：%s", renderMsgs(reqs[1]))
	}
	if !strings.Contains(out, "复核后的最终结论") {
		t.Fatalf("复核后再走一轮的输出应进入流输出，got %q", out)
	}
	req := spy.reqs[0]
	if req.Kind != VerifierKind {
		t.Fatalf("Kind 期望 %q，实际 %q", VerifierKind, req.Kind)
	}
	if !strings.Contains(req.Target, "文件写入证据") || !strings.Contains(req.Target, "最终答案") {
		t.Fatalf("Target 应含最终正文与产出证据标注：%q", req.Target)
	}
	if got := req.VerifierChecks(); len(got) != 4 {
		t.Fatalf("空 Checks 应回落到内置四项，实际 %d 项", len(got))
	}

	// ② hook nil（条件满足）→ 零回归：不送审、不追加复核结果、收尾轮数不变
	spyNil, callsNil, reqsNil, _ := run(false, true)
	if spyNil.count() != 0 {
		t.Fatalf("hook nil 不应调用复核")
	}
	if callsNil != 1 {
		t.Fatalf("hook nil 应 1 次调用正常收尾，实际 %d", callsNil)
	}
	if requestsContain(reqsNil[0], VerifierResultPrefix) {
		t.Fatalf("hook nil 不应注入复核结果：%s", renderMsgs(reqsNil[0]))
	}

	// ③ hook 挂载但无证据（条件不满足）→ 0 次，且首轮请求与 hook nil 逐字一致
	spyNo, callsNo, reqsNo, _ := run(true, false)
	if spyNo.count() != 0 {
		t.Fatalf("无产出证据不应送审")
	}
	if callsNo != 1 {
		t.Fatalf("无证据应 1 次调用正常收尾，实际 %d", callsNo)
	}
	if !sameMessages(reqsNil[0], reqsNo[0]) {
		t.Fatalf("hook nil 与条件不满足的消息序列应逐字一致：\nnil=%s\nnoEv=%s",
			renderMsgs(reqsNil[0]), renderMsgs(reqsNo[0]))
	}
}

// TestMaybeVerifierHandoff_Conditions：送审条件表驱动（逐条独立否定，确认恒返 nil）。
func TestMaybeVerifierHandoff_Conditions(t *testing.T) {
	base := func() *Agent {
		a := &Agent{Task: types.TaskCreate{Mode: "task", Task: "复杂任务"}}
		a.SetThinkPolicy(strictComplexTracker(1))
		a.think.MarkVerified()
		a.sawWriteEvidence = true
		a.lastBody = "最终产出摘要：已实现并测试"
		return a
	}

	cases := []struct {
		name   string
		mutate func(a *Agent)
		want   bool
	}{
		{"strict+complex+已验证+证据 → 触发", func(a *Agent) {}, true},
		{"simple → 不触发", func(a *Agent) {
			p := thinkmgr.BuildPolicy(thinkmgr.Strategy{Policy: "strict", MinRounds: 1, VerifyPass: "on"}, thinkmgr.ComplexitySimple, 10)
			a.SetThinkPolicy(thinkmgr.NewTracker(p, 10, thinkmgr.ComplexitySimple))
		}, false},
		{"auto → 不触发（防成本失控）", func(a *Agent) {
			p := thinkmgr.BuildPolicy(thinkmgr.Strategy{Policy: "auto", VerifyPass: "on"}, thinkmgr.ComplexityComplex, 90)
			a.SetThinkPolicy(thinkmgr.NewTracker(p, 90, thinkmgr.ComplexityComplex))
		}, false},
		{"验证轮未过 → 不触发", func(a *Agent) {
			a.SetThinkPolicy(strictComplexTracker(1)) // 未 MarkVerified
		}, false},
		{"无产出证据 → 不触发", func(a *Agent) {
			a.sawWriteEvidence, a.sawTestEvidence = false, false
		}, false},
		{"goal 模式 → 不触发", func(a *Agent) { a.Task.Mode = "goal" }, false},
		{"复核任务自身 → 不触发（防递归）", func(a *Agent) {
			a.Task.Task = VerifierTaskPrefix + "：对以下产出做只读复核"
		}, false},
		{"已送审 → 不触发（一次上限）", func(a *Agent) { a.verifierDone = true }, false},
		{"策略关闭 → 不触发", func(a *Agent) { a.SetThinkPolicy(nil) }, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := base()
			tc.mutate(a)
			req := a.maybeVerifierHandoff(3)
			if tc.want != (req != nil) {
				t.Fatalf("触发判定不符：want=%v req=%+v", tc.want, req)
			}
			if !tc.want && req == nil && a.verifierDone && tc.name != "已送审 → 不触发（一次上限）" {
				t.Fatalf("未触发不应置 verifierDone")
			}
		})
	}

	// 触发后置 verifierDone（一次上限：第二次恒 nil）
	a := base()
	if a.maybeVerifierHandoff(3) == nil || !a.verifierDone {
		t.Fatal("触发后应置 verifierDone")
	}
	if a.maybeVerifierHandoff(4) != nil {
		t.Fatal("已送审后二次调用应恒返 nil")
	}
}

// TestNoteToolEvidence：按工具名置位产出证据（write_file/replace_in_file/run_tests）。
func TestNoteToolEvidence(t *testing.T) {
	a := &Agent{}
	a.noteToolEvidence([]types.ToolCall{{Function: types.FunctionCall{Name: "read_file"}}})
	if a.sawWriteEvidence || a.sawTestEvidence {
		t.Fatal("read_file 不应置位产出证据")
	}
	a.noteToolEvidence([]types.ToolCall{{Function: types.FunctionCall{Name: "write_file"}}})
	if !a.sawWriteEvidence || a.sawTestEvidence {
		t.Fatal("write_file 应置位文件写入证据")
	}
	a.noteToolEvidence([]types.ToolCall{{Function: types.FunctionCall{Name: "run_tests"}}})
	if !a.sawTestEvidence {
		t.Fatal("run_tests 应置位测试运行证据")
	}
}

// ── M12 用例 ──

// stallTestAgent 构造 auto+complex+NudgeEnabled tracker 的 Agent（纯内存，不跑循环）。
func stallTestAgent(nudgeMax int) *Agent {
	s := thinkmgr.Strategy{Policy: "auto", MaxThinkRounds: 10, NudgeEnabled: true, NudgeMax: nudgeMax}
	p := thinkmgr.BuildPolicy(s, thinkmgr.ComplexityComplex, 80)
	a := &Agent{Task: types.TaskCreate{Mode: "task", Task: "复杂任务"}}
	a.SetThinkPolicy(thinkmgr.NewTracker(p, 80, thinkmgr.ComplexityComplex))
	return a
}

// TestStall_StopNudgingAfterNoEvidence（§2.3 护栏）：连续 2 轮无新证据（无工具调用、
// 结果与正文均无变化）→ 第 3 轮不再注入 nudge；中间有工具调用（结果变化）→ 不 stall。
func TestStall_StopNudgingAfterNoEvidence(t *testing.T) {
	msgs := []types.Message{{Role: "user", Content: "任务"}}
	body := "短答"

	// A：连续 3 轮同一信号（先结算后过门，与主循环同序）→ 第 3 轮应因止损跳过软注入
	a := stallTestAgent(5)
	a.settleThinkRound(0, body, "")
	if _, c1 := a.thinkGate(msgs, body, 1, false); !c1 {
		t.Fatal("A 第 1 轮应软注入（nudge）")
	}
	a.settleThinkRound(0, body, "")
	if _, c2 := a.thinkGate(msgs, body, 2, false); c2 {
		t.Fatal("A 第 2 轮应因间隔不足不注入")
	}
	a.settleThinkRound(0, body, "")
	if !a.thinkStalled || a.stallCount != 2 {
		t.Fatalf("连续 2 轮无新证据应置 thinkStalled（stallCount=2），实际 stalled=%v count=%d", a.thinkStalled, a.stallCount)
	}
	if _, c3 := a.thinkGate(msgs, body, 3, false); c3 {
		t.Fatal("A 第 3 轮（间隔已满足）应因止损跳过软注入")
	}
	if n := a.think.Snapshot().Nudges; n != 1 {
		t.Fatalf("止损后不应再注入（nudges 应保持 1），实际 %d", n)
	}

	// B：中间有工具调用（结果变化）→ 计数清零不 stall，第 3 轮继续注入
	b := stallTestAgent(5)
	b.settleThinkRound(0, body, "")
	if _, c1 := b.thinkGate(msgs, body, 1, false); !c1 {
		t.Fatal("B 第 1 轮应软注入")
	}
	b.settleThinkRound(1, body, "go test ok")
	if b.thinkStalled || b.stallCount != 0 {
		t.Fatalf("有工具调用应清零不 stall，实际 stalled=%v count=%d", b.thinkStalled, b.stallCount)
	}
	if _, c2 := b.thinkGate(msgs, body, 2, false); c2 {
		t.Fatal("B 第 2 轮应因间隔不足不注入")
	}
	b.settleThinkRound(0, body, "")
	if _, c3 := b.thinkGate(msgs, body, 3, false); !c3 {
		t.Fatal("B 第 3 轮未 stall（间隔已满足）应继续软注入")
	}
}

// TestStall_HardInterceptUnaffected（设计文档 §2.3）：止损只停催轮不停拦截——
// thinkStalled 时 strict 硬拦截（①）不受影响。
func TestStall_HardInterceptUnaffected(t *testing.T) {
	a := &Agent{Task: types.TaskCreate{Mode: "task", Task: "复杂任务"}}
	a.SetThinkPolicy(strictComplexTracker(5)) // MinRounds=5：收尾必然未达 → 硬拦截
	a.thinkStalled = true
	msgs := []types.Message{{Role: "user", Content: "任务"}}

	out, cont := a.thinkGate(msgs, "初步答案", 1, false)
	if !cont || len(out) != len(msgs)+1 {
		t.Fatalf("止损状态下 strict 硬拦截仍应生效：cont=%v len(out)=%d", cont, len(out))
	}
	if n := a.think.Snapshot().Intercepts; n != 1 {
		t.Fatalf("硬拦截计数应为 1，实际 %d", n)
	}
}
