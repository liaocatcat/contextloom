package executor

// 多轮思考策略（M3/M4/M5/M6/M8）executor 层集成测试：
// strict 拦截放行 / 逃生通道 / 零回归 / 计划门与提示词条件化 / think_round 事件上报。
// mock 走纯文本兜底路径（未实现 StreamEx），每轮请求消息快照可直接比对。

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/thinkmgr"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 测试基建 ──

// thinkMockBackend 多轮脚本化 mock：按调用序号返回预设正文，并记录每次请求的消息快照。
type thinkMockBackend struct {
	mu       sync.Mutex
	replies  []string
	calls    int
	requests [][]types.Message
}

func newThinkMock(replies ...string) *thinkMockBackend {
	return &thinkMockBackend{replies: replies}
}

// next 记录本次请求并返回本轮脚本正文（超出脚本长度复用最后一条）。
func (m *thinkMockBackend) next(messages []types.Message) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, append([]types.Message(nil), messages...))
	i := m.calls
	m.calls++
	if len(m.replies) == 0 {
		return ""
	}
	if i >= len(m.replies) {
		i = len(m.replies) - 1
	}
	return m.replies[i]
}

// snapshot 返回（调用次数, 每次请求的消息快照）。
func (m *thinkMockBackend) snapshot() (int, [][]types.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls, m.requests
}

func (m *thinkMockBackend) Chat(_ context.Context, messages []types.Message, _ []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.next(messages), nil, nil
}

func (m *thinkMockBackend) Stream(_ context.Context, messages []types.Message) (<-chan string, <-chan error) {
	reply := m.next(messages)
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	if reply != "" {
		ch <- reply
	}
	close(ch)
	close(errCh)
	return ch, errCh
}

func (m *thinkMockBackend) ModelID() string                 { return "think-mock" }
func (m *thinkMockBackend) MaxContext() int                 { return 4096 }
func (m *thinkMockBackend) SupportsVision() bool            { return false }
func (m *thinkMockBackend) SetReasoningEffort(level string) {}
func (m *thinkMockBackend) ReasoningEffort() string         { return "" }
func (m *thinkMockBackend) SetTopP(p float64)               {}
func (m *thinkMockBackend) TopP() float64                   { return 0 }
func (m *thinkMockBackend) SetTemperature(t float64)        {}
func (m *thinkMockBackend) Temperature() float64            { return 0 }

// newThinkTestAgent 构造最小可运行 Agent（chat 模式）。
func newThinkTestAgent(mb *thinkMockBackend, taskText string) *Agent {
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	return &Agent{
		ID:      "think-test-agent",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: taskText},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: mb,
	}
}

// runStreamAndDrain 运行 RunStream 并收集流式输出（断言无流错误）。
func runStreamAndDrain(t *testing.T, a *Agent) string {
	t.Helper()
	ch, errCh, err := a.RunStream(context.Background())
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	var out strings.Builder
	for token := range ch {
		out.WriteString(token)
	}
	select {
	case e := <-errCh:
		if e != nil {
			t.Fatalf("stream err: %v", e)
		}
	default:
	}
	return out.String()
}

// strictComplexTracker strict 策略 + complex 任务（verify_pass=on；nudge 关闭，聚焦拦截路径）。
func strictComplexTracker(minRounds int) *thinkmgr.Tracker {
	s := thinkmgr.Strategy{Policy: "strict", MinRounds: minRounds, MaxThinkRounds: 3, VerifyPass: "on"}
	p := thinkmgr.BuildPolicy(s, thinkmgr.ComplexityComplex, 100)
	return thinkmgr.NewTracker(p, 100, thinkmgr.ComplexityComplex)
}

// contentString 提取消息正文（Content 为 any：文本消息为 string，多模态为 []ContentPart）。
func contentString(m types.Message) string {
	if s, ok := m.Content.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", m.Content)
}

func requestsContain(msgs []types.Message, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(contentString(m), sub) {
			return true
		}
	}
	return false
}

func countMessagesContaining(msgs []types.Message, sub string) int {
	n := 0
	for _, m := range msgs {
		if strings.Contains(contentString(m), sub) {
			n++
		}
	}
	return n
}

func systemContent(msgs []types.Message) string {
	for _, m := range msgs {
		if m.Role == "system" {
			return contentString(m)
		}
	}
	return ""
}

func sameMessages(a, b []types.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || contentString(a[i]) != contentString(b[i]) {
			return false
		}
	}
	return true
}

func renderMsgs(msgs []types.Message) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString(" | ")
		}
		b.WriteString(m.Role + ":" + clip(contentString(m), 60))
	}
	return b.String()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ── 测试 ──

// TestStrictPolicy_Intercept（§6.1 #4）strict+min=2：mock 首轮非空回答被拦截并注入
// CoVe 验证指令，第 2 轮（验证轮）输出 [THINK_VERIFIED] 后放行收尾。
func TestStrictPolicy_Intercept(t *testing.T) {
	mb := newThinkMock("初步答案", "最终答案 [THINK_VERIFIED]")
	agent := newThinkTestAgent(mb, "复杂任务：多步骤实现与验证")
	tracker := strictComplexTracker(2)
	agent.SetThinkPolicy(tracker)

	out := runStreamAndDrain(t, agent)

	calls, reqs := mb.snapshot()
	if calls != 2 {
		t.Fatalf("应恰好 2 次模型调用（首轮拦截 + 验证轮放行），got %d", calls)
	}
	if requestsContain(reqs[0], "【验证轮") {
		t.Fatalf("首轮请求不应包含验证指令：%s", renderMsgs(reqs[0]))
	}
	if !requestsContain(reqs[1], "【验证轮 · 第 2 轮】") {
		t.Fatalf("第 2 次请求应包含验证指令：%s", renderMsgs(reqs[1]))
	}
	if !strings.Contains(out, "最终答案") {
		t.Fatalf("流输出应包含验证轮最终答案，got %q", out)
	}

	st := tracker.Snapshot()
	if st.Intercepts != 1 || st.Nudges != 0 {
		t.Fatalf("拦截/续思计数异常: intercepts=%d nudges=%d", st.Intercepts, st.Nudges)
	}
	if !st.Verified {
		t.Fatalf("验证轮后应标记 Verified: %+v", st)
	}
	if len(st.Rounds) != 2 || st.Rounds[0].Kind != thinkmgr.KindExplore || st.Rounds[1].Kind != thinkmgr.KindVerify {
		t.Fatalf("轮次类型应为 explore→verify: %+v", st.Rounds)
	}
	if st.Rounds[0].BodyLen != len([]rune("初步答案")) {
		t.Fatalf("首轮正文长度应被结算: %+v", st.Rounds[0])
	}
}

// TestIntercept_EscapeHatches（§6.1 #5）逃生通道表驱动：forcedFinal / goal / simple /
// 已达续轮上限 / 策略关闭，均零注入放行（thinkGate 原样返回 false）。
func TestIntercept_EscapeHatches(t *testing.T) {
	base := []types.Message{{Role: "user", Content: "任务"}}
	body := "这是一段足够长的初步回答正文，用于触发可能的拦截判定。"

	cases := []struct {
		name        string
		mode        string
		tracker     *thinkmgr.Tracker
		forcedFinal bool
	}{
		{
			name:        "forcedFinal 不拦截",
			tracker:     strictComplexTracker(5), // 无逃生通道时本应触发拦截 + 软续思
			forcedFinal: true,
		},
		{
			name:    "goal 模式不拦截",
			mode:    "goal",
			tracker: strictComplexTracker(5),
		},
		{
			name: "simple 任务不注入",
			tracker: thinkmgr.NewTracker(
				thinkmgr.BuildPolicy(
					thinkmgr.Strategy{Policy: "strict", MinRounds: 2, VerifyPass: "on", NudgeEnabled: true},
					thinkmgr.ComplexitySimple, 20),
				20, thinkmgr.ComplexitySimple),
		},
		{
			name: "已达续轮上限不拦截",
			tracker: thinkmgr.NewTracker(thinkmgr.Policy{
				Enabled: true, Mode: "strict", Complexity: thinkmgr.ComplexityComplex,
				MinRounds: 5, MaxExtraRounds: 0, VerifyPass: "on", NudgeEnabled: true,
				NudgeMax: 2, NudgeGap: 2, NudgeMinRunes: 200, StallRounds: 2,
			}, 100, thinkmgr.ComplexityComplex),
		},
		{
			name: "策略关闭不拦截",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{Task: types.TaskCreate{Mode: tc.mode, Task: "x"}}
			a.SetThinkPolicy(tc.tracker)
			out, cont := a.thinkGate(base, body, 1, tc.forcedFinal)
			if cont || len(out) != len(base) {
				t.Fatalf("应零注入放行：cont=%v len(out)=%d", cont, len(out))
			}
			if tc.tracker != nil {
				st := tc.tracker.Snapshot()
				if st.Intercepts != 0 || st.Nudges != 0 {
					t.Fatalf("不应有任何注入计数: %+v", st)
				}
			}
		})
	}
}

// TestThinkGate_OffZeroRegression（§6.1 #9）零回归：同一 mock 流开/关两遍，
// 消息序列与请求数逐字一致——挂载 tracker 不改变未触发路径
// （含复杂度零注入守卫与提示词条件化短路）。
func TestThinkGate_OffZeroRegression(t *testing.T) {
	run := func(attach *thinkmgr.Tracker) (int, [][]types.Message) {
		// 首轮空正文 → continue（覆盖多轮循环路径）；第 2 轮实质回答收尾。
		mb := newThinkMock("", "稳定答案")
		agent := newThinkTestAgent(mb, "零回归对照任务")
		if attach != nil {
			agent.SetThinkPolicy(attach)
		}
		runStreamAndDrain(t, agent)
		return mb.snapshot()
	}

	offCalls, offReqs := run(nil)
	// 开启侧使用"强软触发参数 + simple 复杂度"的 tracker：若复杂度零注入守卫
	// 或提示词条件化任一失效，消息序列将不一致。
	onTracker := thinkmgr.NewTracker(
		thinkmgr.BuildPolicy(
			thinkmgr.Strategy{Policy: "strict", MinRounds: 2, VerifyPass: "on", NudgeEnabled: true},
			thinkmgr.ComplexitySimple, 20),
		20, thinkmgr.ComplexitySimple)
	onCalls, onReqs := run(onTracker)

	if offCalls != onCalls || offCalls != 2 {
		t.Fatalf("请求数应一致且为 2：off=%d on=%d", offCalls, onCalls)
	}
	if len(offReqs) != len(onReqs) {
		t.Fatalf("请求次数不一致：off=%d on=%d", len(offReqs), len(onReqs))
	}
	for i := range offReqs {
		if !sameMessages(offReqs[i], onReqs[i]) {
			t.Fatalf("第 %d 次请求消息序列不一致：\noff=%s\non=%s", i+1, renderMsgs(offReqs[i]), renderMsgs(onReqs[i]))
		}
	}
}

// TestPlanGate_SoftInject（§6.1 #8）计划门（M8 soft）+ 提示词条件化（H6）：
// complex 首轮注入 1 条【执行计划】提示且收尾指令条件化；simple 零注入且提示词保持原文。
func TestPlanGate_SoftInject(t *testing.T) {
	newRun := func(c thinkmgr.Complexity, score int) (*thinkMockBackend, *Agent) {
		mb := newThinkMock("答案")
		agent := newThinkTestAgent(mb, "任务文本")
		p := thinkmgr.BuildPolicy(thinkmgr.Strategy{Policy: "auto"}, c, score)
		agent.SetThinkPolicy(thinkmgr.NewTracker(p, score, c))
		runStreamAndDrain(t, agent)
		return mb, agent
	}

	mbC, agentC := newRun(thinkmgr.ComplexityComplex, 80)
	callsC, reqsC := mbC.snapshot()
	if callsC != 1 {
		t.Fatalf("complex 应 1 次请求收尾，got %d", callsC)
	}
	if n := countMessagesContaining(reqsC[0], "【执行计划】"); n != 1 {
		t.Fatalf("complex 首轮应恰好注入 1 条计划提示，got %d：%s", n, renderMsgs(reqsC[0]))
	}
	if !requestsContain(reqsC[0], "【计划完】") {
		t.Fatalf("计划提示应包含【计划完】标记：%s", renderMsgs(reqsC[0]))
	}
	if !agentC.planOutlined || agentC.planOutline == "" {
		t.Fatalf("计划门状态应登记：outlined=%v outline=%q", agentC.planOutlined, agentC.planOutline)
	}
	if sys := systemContent(reqsC[0]); !strings.Contains(sys, "复杂问题先列计划、执行后自查再收尾") {
		t.Fatalf("complex 收尾指令应条件化，system=%q", clip(sys, 200))
	}

	mbS, agentS := newRun(thinkmgr.ComplexitySimple, 20)
	callsS, reqsS := mbS.snapshot()
	if callsS != 1 {
		t.Fatalf("simple 应 1 次请求收尾，got %d", callsS)
	}
	if n := countMessagesContaining(reqsS[0], "【执行计划】"); n != 0 {
		t.Fatalf("simple 不应注入计划提示，got %d", n)
	}
	if agentS.planOutlined {
		t.Fatalf("simple 不应登记 planOutlined")
	}
	if sys := systemContent(reqsS[0]); !strings.Contains(sys, "任务完成后直接给出最终答案并停止") {
		t.Fatalf("simple 收尾指令应保持原文，system=%q", clip(sys, 200))
	}
}

// thinkTraceRecorder 记录 think_round 事件（同时实现 Tracer 与可选扩展 thinkRoundTracer）。
type thinkTraceRecorder struct {
	mu      sync.Mutex
	rounds  []int
	details []string
}

func (r *thinkTraceRecorder) TraceRound(mode string, round int, tool string, ms int64, errMsg string) {
}

func (r *thinkTraceRecorder) TraceThinkRound(mode string, round int, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds = append(r.rounds, round)
	r.details = append(r.details, detail)
}

// plainTracer 仅实现 Tracer 本体（未实现可选扩展）：think_round 上报应静默跳过。
type plainTracer struct{}

func (plainTracer) TraceRound(mode string, round int, tool string, ms int64, errMsg string) {
}

// TestThinkRound_TraceHook 可选 Tracer 扩展（M11）：每轮结算上报 1 条 think_round，
// detail 为 thinkmgr.ThinkRound JSON 且可解析；未实现扩展的 Tracer 静默跳过。
func TestThinkRound_TraceHook(t *testing.T) {
	mb := newThinkMock("初步答案", "最终答案 [THINK_VERIFIED]")
	agent := newThinkTestAgent(mb, "复杂任务")
	agent.SetThinkPolicy(strictComplexTracker(2))
	rec := &thinkTraceRecorder{}
	agent.SetTracer(rec)

	runStreamAndDrain(t, agent)

	rec.mu.Lock()
	rounds := append([]int(nil), rec.rounds...)
	details := append([]string(nil), rec.details...)
	rec.mu.Unlock()

	if len(rounds) != 2 || rounds[0] != 1 || rounds[1] != 2 {
		t.Fatalf("应逐轮上报 think_round 1/2，got %v", rounds)
	}
	var first, second thinkmgr.ThinkRound
	if err := json.Unmarshal([]byte(details[0]), &first); err != nil {
		t.Fatalf("detail 应为 thinkmgr.ThinkRound JSON: %v (%q)", err, details[0])
	}
	if err := json.Unmarshal([]byte(details[1]), &second); err != nil {
		t.Fatalf("detail 应为 thinkmgr.ThinkRound JSON: %v (%q)", err, details[1])
	}
	if first.Round != 1 || first.Kind != thinkmgr.KindExplore || first.HasTools {
		t.Fatalf("首轮事件字段异常: %+v", first)
	}
	if second.Round != 2 || second.Kind != thinkmgr.KindVerify {
		t.Fatalf("验证轮事件字段异常: %+v", second)
	}

	// 未实现可选扩展的 Tracer：类型断言失败静默跳过（不 panic）。
	agent.SetTracer(plainTracer{})
	agent.traceThinkRound()
}
