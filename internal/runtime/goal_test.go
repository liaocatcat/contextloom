package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── Goal 协议解析测试 ──

func TestExtractGoalPlan(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     int
	}{
		{
			name: "闭合标记",
			response: `分析需求后输出计划：
[GOAL_PLAN]
[{"id":"s1","desc":"分析需求"},{"id":"s2","desc":"实现功能"},{"id":"s3","desc":"编写测试"}]
[/GOAL_PLAN]`,
			want: 3,
		},
		{
			name: "未闭合标记",
			response: `[GOAL_PLAN]
[{"id":"s1","desc":"步骤1"}]
后续内容...`,
			want: 1,
		},
		{
			name:     "无标记",
			response: "直接执行，没有计划输出",
			want:     0,
		},
		{
			name:     "空数组",
			response: "[GOAL_PLAN]\n[]\n[/GOAL_PLAN]",
			want:     0,
		},
		{
			name:     "非法JSON",
			response: "[GOAL_PLAN]\n不是JSON\n[/GOAL_PLAN]",
			want:     0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			steps := extractGoalPlan(c.response)
			if len(steps) != c.want {
				t.Fatalf("步骤数应为 %d，got %d (%v)", c.want, len(steps), steps)
			}
			if len(steps) > 0 && steps[0].ID == "" {
				t.Fatal("步骤应有 id")
			}
			if len(steps) > 0 && steps[0].Status != string(types.GoalStepPending) {
				t.Fatalf("步骤初始状态应为 pending，got %s", steps[0].Status)
			}
		})
	}
}

func TestExtractGoalSteps(t *testing.T) {
	response := `汇报状态：
[GOAL_STEPS]
[{"id":"s1","status":"done","evidence":"文件已修改"},{"id":"s2","status":"in_progress"}]
[/GOAL_STEPS]`
	steps := extractGoalSteps(response)
	if len(steps) != 2 {
		t.Fatalf("应提取 2 个步骤状态，got %d", len(steps))
	}
	if steps[0].Status != string(types.GoalStepDone) || steps[0].Evidence != "文件已修改" {
		t.Fatalf("s1 应为 done+evidence，got %+v", steps[0])
	}
	if steps[1].Status != string(types.GoalStepPending) {
		t.Fatalf("未知状态应回落 pending，got %s", steps[1].Status)
	}
}

func TestExtractGoalSteps_Synonyms(t *testing.T) {
	response := `[GOAL_STEPS]
[{"id":"s1","status":"completed"},{"id":"s2","status":"ok"}]
[/GOAL_STEPS]`
	steps := extractGoalSteps(response)
	if len(steps) != 2 {
		t.Fatalf("应提取 2 个，got %d", len(steps))
	}
	for _, st := range steps {
		if st.Status != string(types.GoalStepDone) {
			t.Fatalf("completed/ok 应归一为 done，got %s", st.Status)
		}
	}
}

// ── GoalOnRoundEnd 集成测试 ──

func TestGoalOnRoundEnd_PlanExtractionAndContinuation(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")
	rt.goalMgr.Start("分析目标", 5, 0)

	// 第一轮：plan 轮响应，输出步骤清单但未完成
	cont, reason := rt.GoalOnRoundEnd(`规划完成：
[GOAL_PLAN]
[{"id":"s1","desc":"分析"},{"id":"s2","desc":"实现"}]
[/GOAL_PLAN]
先完成分析。`, true, 70, types.OverseerReport{})
	if !cont {
		t.Fatalf("plan 轮后应继续，reason=%s", reason)
	}
	gs := rt.GoalSession()
	if gs == nil {
		t.Fatal("GoalSession 应为非 nil")
	}
	if len(gs.Steps) != 2 {
		t.Fatalf("应提取 2 个步骤，got %d", len(gs.Steps))
	}
	if gs.Round != 1 || gs.Phase != types.GoalPhaseExecute {
		t.Fatalf("状态机推进异常: round=%d phase=%s", gs.Round, gs.Phase)
	}
	if gs.Continuation == "" {
		t.Fatal("应生成 continuation")
	}
	if !strings.Contains(gs.Continuation, "s1") && !strings.Contains(gs.Continuation, "s2") {
		t.Fatalf("continuation 应提及未完成步骤，got %q", gs.Continuation)
	}
}

func TestGoalOnRoundEnd_Done(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")
	rt.goalMgr.Start("完成目标", 5, 0)

	// plan 轮
	rt.GoalOnRoundEnd(`[GOAL_PLAN]
[{"id":"s1","desc":"分析"}]
[/GOAL_PLAN]`, true, 70, types.OverseerReport{})
	// 执行轮：汇报 s1 done + [GOAL_DONE] → 先进入打磨轮，不直接结束
	cont, reason := rt.GoalOnRoundEnd(`完成 [GOAL_DONE]`, true, 95, types.OverseerReport{})
	if !cont {
		t.Fatalf("[GOAL_DONE] 应先进入打磨轮继续，reason=%s", reason)
	}
	gs := rt.GoalSession()
	if gs.Phase != types.GoalPhaseCheck {
		t.Fatalf("阶段应为 check（打磨轮），got %s", gs.Phase)
	}
	if !strings.Contains(gs.Continuation, "打磨轮") {
		t.Fatalf("打磨轮 continuation 应含打磨指令，got %q", gs.Continuation)
	}
	// 打磨轮完成：输出 [GOAL_POLISH_DONE] → 真正完成
	cont, reason = rt.GoalOnRoundEnd("打磨完成 [GOAL_POLISH_DONE]", true, 96, types.OverseerReport{})
	if cont {
		t.Fatalf("打磨轮完成后不应继续，reason=%s", reason)
	}
	gs = rt.GoalSession()
	if gs.Phase != types.GoalPhaseDone {
		t.Fatalf("阶段应为 done，got %s", gs.Phase)
	}
	if !gs.Polished {
		t.Fatal("Polished 标记应为 true")
	}
}

func TestGoalOnRoundEnd_MaxRounds(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")

	// 创建 1 轮上限的会话（通过直接构造 Manager 不便，改用 Start 默认并校验轮次判定）
	// 直接操纵 goalMgr：重建一个 1 轮会话
	rt.goalMgr.Start("短目标", 1, 0)
	rt.goalMgr.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤", Status: string(types.GoalStepPending)}})

	// 达到建议轮次后：软提示继续，不强制 failed
	cont, reason := rt.GoalOnRoundEnd("有进展", true, 70, types.OverseerReport{})
	if !cont {
		t.Fatalf("达到建议轮次应继续执行（软提示），reason=%s", reason)
	}
	if rt.GoalSession().Phase == types.GoalPhaseFailed {
		t.Fatal("达到建议轮次不应置为 failed")
	}
	if !strings.Contains(rt.GoalSession().Continuation, "轮次提示") {
		t.Fatalf("continuation 应含轮次提示，got %q", rt.GoalSession().Continuation)
	}
}

func TestGoalOnRoundEnd_NoActiveGoal(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	cont, reason := rt.GoalOnRoundEnd("无目标", true, 70, types.OverseerReport{})
	if cont {
		t.Fatalf("无活动目标不应继续，reason=%s", reason)
	}
}

func TestGoalAbortAndList(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")

	msg := rt.GoalAbort()
	if !strings.Contains(msg, "无活动目标") {
		t.Fatalf("无目标时 abort 应提示，got %q", msg)
	}

	// 创建会话（ProcessInput 绑定）后 abort
	rt.goalMgr.Start("测试目标", 5, 0)
	msg = rt.GoalAbort()
	if !strings.Contains(msg, "已中止") {
		t.Fatalf("abort 应提示已中止，got %q", msg)
	}
	if !rt.GoalSession().Done() {
		t.Fatal("abort 后会话应标记终止")
	}

	// list 应包含历史
	list := rt.GoalList()
	if !strings.Contains(list, "测试目标") {
		t.Fatalf("list 应包含目标，got %q", list)
	}
}

func TestGoalLoad_RestoresSession(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")

	gs := rt.goalMgr.Start("可恢复目标", 5, 0)
	rt.goalMgr.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})
	rt.goalMgr.ApplyRoundResult("进展", false, 60, 100, nil)
	rt.SetMode("chat") // 离开 goal（会话重置为 nil）
	if rt.GoalSession() != nil {
		t.Fatal("切到 chat 后 GoalSession 应为 nil")
	}

	msg, err := rt.GoalLoad(gs.ID)
	if err != nil {
		t.Fatalf("GoalLoad 失败: %v", err)
	}
	if !strings.Contains(msg, "可恢复目标") {
		t.Fatalf("加载消息应含目标名，got %q", msg)
	}
	loaded := rt.GoalSession()
	if loaded == nil || loaded.Round != 1 || len(loaded.Checkpoints) != 1 {
		t.Fatalf("恢复的会话状态不符: %+v", loaded)
	}
	if rt.GetMode() != "goal" {
		t.Fatalf("GoalLoad 应切回 goal 模式，got %s", rt.GetMode())
	}
}

func TestGoalStatus_Format(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")

	status := rt.GoalStatus()
	if !strings.Contains(status, "无活动目标") {
		t.Fatalf("无目标状态应提示，got %q", status)
	}

	rt.goalMgr.Start("格式化目标", 5, 1000)
	rt.goalMgr.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})
	status = rt.GoalStatus()
	for _, want := range []string{"格式化目标", "步骤1", "轮 0/5"} {
		if !strings.Contains(status, want) {
			t.Fatalf("状态应包含 %q，got:\n%s", want, status)
		}
	}
}

// ── ProcessGoalRound 集成测试 ──

func TestProcessGoalRound_ContinuationInjected(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")

	gs := rt.goalMgr.Start("集成目标", 5, 0)
	rt.goalMgr.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})
	rt.goalMgr.ApplyRoundResult("第一轮", true, 70, 100, nil)
	if gs.Continuation == "" {
		t.Fatal("应已有 continuation")
	}

	// 下一轮应注入 continuation 到对话历史
	ch, errCh, err := rt.ProcessGoalRound(context.Background())
	if err != nil {
		t.Fatalf("ProcessGoalRound 启动失败: %v", err)
	}
	// 消费直到关闭
	for range ch {
	}
	for e := range errCh {
		if e != nil {
			t.Fatalf("流错误: %v", e)
		}
	}
	hist := rt.GetConversationHistory()
	found := false
	for _, h := range hist {
		if strings.Contains(fmt.Sprint(h.Content), "目标循环自动续跑") {
			found = true
		}
	}
	if !found {
		t.Fatal("对话历史应包含自动续跑指令")
	}
}

func TestProcessGoalRound_NoSession(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	_, _, err := rt.ProcessGoalRound(context.Background())
	if err == nil {
		t.Fatal("无活动目标应报错")
	}
}

func TestProcessGoalRound_DoneSession(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	gs := rt.goalMgr.Start("已完成目标", 5, 0)
	rt.goalMgr.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone)}})
	// [GOAL_DONE] 先进入打磨轮，打磨完成后才真正终止
	rt.goalMgr.ApplyRoundResult("[GOAL_DONE]", true, 95, 100, nil)
	rt.goalMgr.ApplyRoundResult("[GOAL_POLISH_DONE]", true, 95, 100, nil)
	if !gs.Done() {
		t.Fatal("会话应已终止")
	}
	_, _, err := rt.ProcessGoalRound(context.Background())
	if err == nil {
		t.Fatal("已终止会话不应再启动轮次")
	}
}

// ── 多会话隔离集成测试（goal 目标按对话会话隔离，修复"新建会话串目标"）──

func TestProcessInput_GoalSessionIsolation(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")

	// 会话 A：发消息 → 自动建立目标 A
	sA := rt.NewSession("会话A")
	consume := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("启动失败: %v", err)
		}
	}
	ch, errCh, err := rt.ProcessInput(context.Background(), "目标A：完成功能X")
	consume(err)
	for range ch {
	}
	for e := range errCh {
		if e != nil {
			t.Fatalf("流错误: %v", e)
		}
	}
	gs := rt.GoalSession()
	if gs == nil || gs.Goal != "目标A：完成功能X" {
		t.Fatalf("会话 A 应建立目标 A，got %+v", gs)
	}
	if gs.SessionID != sA.UUID {
		t.Fatalf("目标 A 应归属会话 A，got %q", gs.SessionID)
	}

	// 新建会话 B：不应看到 A 的目标（隔离）
	sB := rt.NewSession("会话B")
	if rt.GoalSession() != nil {
		t.Fatalf("会话 B 不应看到会话 A 的目标（串扰）")
	}

	// 会话 B 发消息 → 自动建立自己的目标 B（不再是推进 A 的指令）
	ch, errCh, err = rt.ProcessInput(context.Background(), "目标B：完成功能Y")
	consume(err)
	for range ch {
	}
	for e := range errCh {
		if e != nil {
			t.Fatalf("流错误: %v", e)
		}
	}
	gs = rt.GoalSession()
	if gs == nil || gs.Goal != "目标B：完成功能Y" {
		t.Fatalf("会话 B 应建立自己的目标 B，got %+v", gs)
	}
	if gs.SessionID != sB.UUID {
		t.Fatalf("目标 B 应归属会话 B，got %q", gs.SessionID)
	}

	// 切回会话 A：恢复 A 的目标（隔离 + 进度保留，不丢）
	if _, _, err := rt.LoadSession(sA.UUID); err != nil {
		t.Fatalf("LoadSession(A) 失败: %v", err)
	}
	gs = rt.GoalSession()
	if gs == nil || gs.Goal != "目标A：完成功能X" {
		t.Fatalf("切回 A 应恢复目标 A，got %+v", gs)
	}
	if gs.SessionID != sA.UUID {
		t.Fatalf("恢复的目标 A 应归属会话 A，got %q", gs.SessionID)
	}
}

// ── v2 自进化协议解析测试 ──

func TestExtractGoalReflect(t *testing.T) {
	refl := extractGoalReflect(`本轮完成分析。
[GOAL_REFLECT]
{"summary":"完成 s1 分析","problems":"s2 依赖未确认","next_plan":"先确认依赖再实现"}
[/GOAL_REFLECT]`)
	if refl.Summary != "完成 s1 分析" || refl.Problems != "s2 依赖未确认" || refl.NextPlan != "先确认依赖再实现" {
		t.Fatalf("反思解析异常: %+v", refl)
	}
	// 无标记 → 空
	if empty := extractGoalReflect("普通响应"); empty.Summary != "" {
		t.Fatalf("无标记应返回空，got %+v", empty)
	}
}

func TestExtractGoalDefects(t *testing.T) {
	defects := extractGoalDefects(`修复完成：
[GOAL_DEFECTS]
[{"id":"d-test","status":"fixed","evidence":"新增测试全过"},{"id":"d-quality","status":"open"}]
[/GOAL_DEFECTS]`)
	if len(defects) != 2 {
		t.Fatalf("应解析 2 条缺陷，got %d", len(defects))
	}
	if defects[0].ID != "d-test" || defects[0].Status != "fixed" || defects[0].Evidence == "" {
		t.Fatalf("缺陷 1 解析异常: %+v", defects[0])
	}
	if defects[1].ID != "d-quality" || defects[1].Status != "open" {
		t.Fatalf("缺陷 2 解析异常: %+v", defects[1])
	}
}

func TestExtractGoalLessons(t *testing.T) {
	// 单对象形态
	if ls := extractGoalLessons(`[GOAL_LESSON]{"lesson":"先跑测试再改代码"}[/GOAL_LESSON]`); len(ls) != 1 || ls[0] != "先跑测试再改代码" {
		t.Fatalf("单对象经验解析异常: %v", ls)
	}
	// 数组形态
	if ls := extractGoalLessons(`[GOAL_LESSON][{"lesson":"a"},{"lesson":"b"}][/GOAL_LESSON]`); len(ls) != 2 {
		t.Fatalf("数组经验解析异常: %v", ls)
	}
	// 无标记
	if ls := extractGoalLessons("普通"); ls != nil {
		t.Fatalf("无标记应返回 nil，got %v", ls)
	}
}

func TestExtractGoalPlan_WithAcceptance(t *testing.T) {
	steps := extractGoalPlan(`[GOAL_PLAN]
[{"id":"s1","desc":"实现登录","acceptance":"登录接口 200 且测试通过"}]
[/GOAL_PLAN]`)
	if len(steps) != 1 || steps[0].Acceptance != "登录接口 200 且测试通过" {
		t.Fatalf("plan 应提取验收标准，got %+v", steps)
	}
	// 旧格式（无 acceptance）兼容
	old := extractGoalPlan(`[GOAL_PLAN]
[{"id":"s1","desc":"实现登录"}]
[/GOAL_PLAN]`)
	if len(old) != 1 || old[0].Acceptance != "" {
		t.Fatalf("旧格式应兼容（acceptance 为空），got %+v", old)
	}
}

func TestHasGoalPolishDone(t *testing.T) {
	if !hasGoalPolishDone("打磨完成 [GOAL_POLISH_DONE]") {
		t.Fatal("应识别打磨完成标记")
	}
	if hasGoalPolishDone("普通响应") {
		t.Fatal("不应误识别")
	}
}

func TestDefectsFromReport(t *testing.T) {
	// 通过 → 无缺陷
	if d := defectsFromReport(types.OverseerReport{Completeness: 95, TestCoverage: 95, CodeQuality: 90, NoSideEffect: 92}, true); len(d) != 0 {
		t.Fatalf("通过时不应生成缺陷，got %v", d)
	}
	// 未通过 + 低分维度 → 缺陷
	rep := types.OverseerReport{Completeness: 95, TestCoverage: 55, CodeQuality: 70, NoSideEffect: 92}
	d := defectsFromReport(rep, false)
	if len(d) != 2 {
		t.Fatalf("应生成 2 条缺陷（测试+质量），got %d: %v", len(d), d)
	}
	if d[0].ID != "d-test" || d[1].ID != "d-quality" {
		t.Fatalf("缺陷 ID 异常: %v", d)
	}
	// 全低分 → 4 条
	rep2 := types.OverseerReport{Completeness: 60, TestCoverage: 50, CodeQuality: 40, NoSideEffect: 70}
	if d := defectsFromReport(rep2, false); len(d) != 4 {
		t.Fatalf("应生成 4 条缺陷，got %d", len(d))
	}
}

// TestGoalOnRoundEnd_V2FullChain 模拟 3 轮完整链路：
// plan(带验收) → 执行未通过(监工缺陷+反思) → 返工修复(缺陷划掉) → 完成(打磨轮) → 打磨完成。
func TestGoalOnRoundEnd_V2FullChain(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")
	rt.goalMgr.Start("实现功能", 10, 0)

	// R1: plan 轮，带验收标准
	cont, _ := rt.GoalOnRoundEnd(`[GOAL_PLAN]
[{"id":"s1","desc":"实现登录","acceptance":"接口 200+测试通过"}]
[/GOAL_PLAN]`, true, 75, types.OverseerReport{})
	if !cont {
		t.Fatal("plan 轮后应继续")
	}
	gs := rt.GoalSession()
	if len(gs.Steps) != 1 || gs.Steps[0].Acceptance != "接口 200+测试通过" {
		t.Fatalf("验收标准未提取: %+v", gs.Steps)
	}

	// R2: 执行未通过 → 监工低分转缺陷 + 反思
	report := types.OverseerReport{Completeness: 95, TestCoverage: 55, CodeQuality: 88, NoSideEffect: 92}
	cont, reason := rt.GoalOnRoundEnd(`实现完成但未跑测试
[GOAL_REFLECT]
{"summary":"实现了登录","problems":"忘记补测试","next_plan":"补测试"}
[/GOAL_REFLECT]`, false, 60, report)
	if !cont {
		t.Fatalf("未通过应继续，reason=%s", reason)
	}
	gs = rt.GoalSession()
	if len(gs.OpenDefects()) != 1 || gs.OpenDefects()[0].ID != "d-test" {
		t.Fatalf("监工低分应转缺陷，got %v", gs.OpenDefects())
	}
	if len(gs.Reflections) != 1 || gs.Reflections[0].Problems != "忘记补测试" {
		t.Fatalf("反思应记录，got %v", gs.Reflections)
	}

	// R3: 返工修复 → 缺陷划掉 + 步骤 done(带证据)
	cont, _ = rt.GoalOnRoundEnd(`已补测试并全部通过
[GOAL_STEPS]
[{"id":"s1","status":"done","evidence":"go test 3 个用例全过"}]
[/GOAL_STEPS]
[GOAL_DEFECTS]
[{"id":"d-test","status":"fixed","evidence":"新增 3 测试"}]
[/GOAL_DEFECTS]`, true, 92, types.OverseerReport{})
	if !cont {
		t.Fatal("修复后应继续")
	}
	gs = rt.GoalSession()
	if len(gs.OpenDefects()) != 0 {
		t.Fatalf("缺陷应全部修复，got %v", gs.OpenDefects())
	}
	if gs.DoneSteps() != 1 || gs.Steps[0].Evidence == "" {
		t.Fatalf("步骤应有效 done，got %+v", gs.Steps)
	}
}

// ── v2 端到端验收：脚本化模型驱动完整自进化闭环 ──
// 真实 runtime + 真实 Overseer 评估 + 真实状态机，脚本化 backend 模拟模型输出。
// 链路：plan(带验收) → 执行未通过(监工低分转缺陷+反思) → 返工修复(缺陷划掉+证据) →
//       步骤有效完成 → 打磨轮 → [GOAL_POLISH_DONE] 正式完成。

// scriptedBackend 按调用顺序返回预置响应（模拟模型多轮输出）。
type scriptedBackend struct {
	mu        sync.Mutex
	responses []string
	idx       int
}

func (m *scriptedBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.responses) {
		return "done", nil, nil
	}
	r := m.responses[m.idx]
	m.idx++
	return r, nil, nil
}

func (m *scriptedBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	m.mu.Lock()
	resp := "done"
	if m.idx < len(m.responses) {
		resp = m.responses[m.idx]
		m.idx++
	}
	m.mu.Unlock()
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		ch <- resp
	}()
	return ch, errCh
}

func (m *scriptedBackend) ModelID() string                 { return "mock-model" }
func (m *scriptedBackend) MaxContext() int                 { return 4096 }
func (m *scriptedBackend) SupportsVision() bool            { return false }
func (m *scriptedBackend) SetReasoningEffort(level string) {}
func (m *scriptedBackend) ReasoningEffort() string         { return "max" }
func (m *scriptedBackend) SetTopP(p float64)               {}
func (m *scriptedBackend) TopP() float64                   { return 0 }
func (m *scriptedBackend) SetTemperature(t float64)        {}
func (m *scriptedBackend) Temperature() float64            { return 0 }

func collectStream(t *testing.T, ch <-chan string, errCh <-chan error) string {
	t.Helper()
	var b strings.Builder
	for {
		select {
		case d, ok := <-ch:
			if !ok {
				return b.String()
			}
			b.WriteString(d)
		case e, ok := <-errCh:
			if ok && e != nil {
				t.Fatalf("流错误: %v", e)
			}
		}
	}
}

func TestGoal_E2E_SelfEvolutionLoop(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")
	rt.Backend = &scriptedBackend{responses: []string{
		// R1 plan：带验收标准
		`[GOAL_PLAN]
[{"id":"s1","desc":"实现统计模块","acceptance":"代码可编译且测试通过"}]
[/GOAL_PLAN]
规划完成，开始实现。`,
		// R2 执行：代码任务但无测试 → 监工未通过 → 自动缺陷 + 反思
		"```go\nfunc Add(a, b int) int { return a + b }\n```\n实现完成\n[GOAL_REFLECT]{\"summary\":\"实现了 Add\",\"problems\":\"忘记写测试\",\"next_plan\":\"补单元测试\"}[/GOAL_REFLECT]",
		// R3 返工：补测试 → 缺陷划掉 + 步骤 done 带证据 → 触发打磨轮
		"补了单元测试，全部测试通过。\n[GOAL_STEPS][{\"id\":\"s1\",\"status\":\"done\",\"evidence\":\"go test 3 个用例全过\"}][/GOAL_STEPS]\n[GOAL_DEFECTS][{\"id\":\"d-test\",\"status\":\"fixed\",\"evidence\":\"新增 3 个测试\"}][/GOAL_DEFECTS]\n[GOAL_REFLECT]{\"summary\":\"补测试\",\"problems\":\"\",\"next_plan\":\"\"}[/GOAL_REFLECT]",
		// R4 打磨轮完成
		"检查了边界情况与文档，打磨完成。\n[GOAL_POLISH_DONE]",
	}}

	ctx := context.Background()
	// R1：plan 轮（ProcessInput）
	ch, errCh, err := rt.ProcessInput(ctx, "实现统计模块")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	r1 := collectStream(t, ch, errCh)
	passed1, score1, rep1 := rt.OverseerEvaluate(r1)
	cont, reason := rt.GoalOnRoundEnd(r1, passed1, score1, rep1)
	if !cont {
		t.Fatalf("R1 plan 轮后应继续，reason=%s", reason)
	}
	gs := rt.GoalSession()
	if len(gs.Steps) != 1 || gs.Steps[0].Acceptance == "" {
		t.Fatalf("R1 应提取验收标准，got %+v", gs.Steps)
	}

	// R2：执行轮（ProcessGoalRound 自动续跑）
	ch, errCh, err = rt.ProcessGoalRound(ctx)
	if err != nil {
		t.Fatalf("ProcessGoalRound R2: %v", err)
	}
	r2 := collectStream(t, ch, errCh)
	passed2, score2, rep2 := rt.OverseerEvaluate(r2)
	if passed2 {
		t.Fatalf("R2 应未通过（代码任务无测试），score=%.0f", score2)
	}
	cont, reason = rt.GoalOnRoundEnd(r2, passed2, score2, rep2)
	if !cont {
		t.Fatalf("R2 未通过应继续返工，reason=%s", reason)
	}
	gs = rt.GoalSession()
	if len(gs.OpenDefects()) == 0 {
		t.Fatal("R2 未通过应自动生成缺陷清单")
	}
	if !strings.Contains(gs.Continuation, "缺陷清单") {
		t.Fatalf("continuation 应含缺陷清单，got %q", gs.Continuation)
	}
	if len(gs.Reflections) != 1 || gs.Reflections[0].Problems != "忘记写测试" {
		t.Fatalf("R2 反思应记录，got %+v", gs.Reflections)
	}

	// R3：返工修复（ProcessGoalRound）
	ch, errCh, err = rt.ProcessGoalRound(ctx)
	if err != nil {
		t.Fatalf("ProcessGoalRound R3: %v", err)
	}
	r3 := collectStream(t, ch, errCh)
	passed3, score3, rep3 := rt.OverseerEvaluate(r3)
	if !passed3 {
		t.Fatalf("R3 应通过（补测试），score=%.0f", score3)
	}
	cont, reason = rt.GoalOnRoundEnd(r3, passed3, score3, rep3)
	if !cont {
		t.Fatalf("R3 全部完成应进入打磨轮继续，reason=%s", reason)
	}
	gs = rt.GoalSession()
	if len(gs.OpenDefects()) != 0 {
		t.Fatalf("R3 缺陷应全部修复，got %v", gs.OpenDefects())
	}
	if gs.Phase != types.GoalPhaseCheck {
		t.Fatalf("R3 后应进入打磨轮（check），got %s", gs.Phase)
	}
	if !strings.Contains(gs.Continuation, "打磨轮") {
		t.Fatalf("打磨轮 continuation 应含打磨指令，got %q", gs.Continuation)
	}

	// R4：打磨轮完成（ProcessGoalRound）
	ch, errCh, err = rt.ProcessGoalRound(ctx)
	if err != nil {
		t.Fatalf("ProcessGoalRound R4: %v", err)
	}
	r4 := collectStream(t, ch, errCh)
	passed4, score4, rep4 := rt.OverseerEvaluate(r4)
	cont, reason = rt.GoalOnRoundEnd(r4, passed4, score4, rep4)
	if cont {
		t.Fatalf("R4 打磨完成后不应继续，reason=%s", reason)
	}
	gs = rt.GoalSession()
	if gs.Phase != types.GoalPhaseDone || !gs.Polished {
		t.Fatalf("R4 后应正式完成，phase=%s polished=%v", gs.Phase, gs.Polished)
	}
	if gs.Round != 4 {
		t.Fatalf("应共 4 轮，got %d", gs.Round)
	}
	// 收敛检测未误报：每轮都有实质进展
	if gs.StalledRounds != 0 {
		t.Fatalf("全链路每轮有进展，停滞应为 0，got %d", gs.StalledRounds)
	}
}

// TestStripGoalProtocol 协议标记剥离：监工评分只看真实正文，防自报刷分。
func TestStripGoalProtocol(t *testing.T) {
	in := "实现完成\n[GOAL_REFLECT]{\"summary\":\"x\",\"problems\":\"忘记写测试\",\"next_plan\":\"补单元测试\"}[/GOAL_REFLECT]\n[GOAL_DEFECTS][{\"id\":\"d-test\",\"status\":\"fixed\",\"evidence\":\"新增 3 个测试\"}][/GOAL_DEFECTS]\n[GOAL_DONE]"
	got := stripGoalProtocol(in)
	for _, banned := range []string{"GOAL_REFLECT", "GOAL_DEFECTS", "单元测试", "GOAL_DONE"} {
		if strings.Contains(got, banned) {
			t.Fatalf("剥离后不应含 %q，got %q", banned, got)
		}
	}
	if !strings.Contains(got, "实现完成") {
		t.Fatalf("正文应保留，got %q", got)
	}
	// 未闭合标记：截断到标记起点
	got2 := stripGoalProtocol("正文A\n[GOAL_STEPS][{\"id\":\"s1\"")
	if strings.Contains(got2, "GOAL_STEPS") || !strings.Contains(got2, "正文A") {
		t.Fatalf("未闭合标记应截断，got %q", got2)
	}
}

// ── 缺陷 A/B + 问题 2 回归：重启恢复目标 / resume 归属 / 模式按会话隔离 ──

// TestLoadSession_RestoresGoal 缺陷 A：backend 重启后 LoadSession 应自动恢复
// 该会话磁盘上的未完成目标（此前 bySID 全空 → 发消息静默新建、旧目标失联）。
func TestLoadSession_RestoresGoal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-restore-goal-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 第一代 Runtime：会话 A 建立目标并推进一轮
	rt1 := newTestRuntimeAt(t, tmpDir)
	sA := rt1.NewSession("会话A")
	rt1.SetMode("goal")
	gs := rt1.goalMgr.Start("重启后应恢复的目标", 0, 0)
	rt1.goalMgr.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})
	rt1.goalMgr.ApplyRoundResult("进展中", false, 60, 100, nil)
	rt1.goalMgr.Persist() // 确保落盘

	// 模拟 backend 重启：第二代 Runtime 同目录（bySID 全空）
	rt2 := newTestRuntimeAt(t, tmpDir)
	if _, _, err := rt2.LoadSession(sA.UUID); err != nil {
		t.Fatalf("LoadSession(A) 失败: %v", err)
	}
	restored := rt2.GoalSession()
	if restored == nil || restored.ID != gs.ID {
		t.Fatalf("重启后 LoadSession 应自动恢复目标 %s（缺陷 A），got %+v", gs.ID, restored)
	}
	if restored.Round != 1 || len(restored.Steps) != 1 {
		t.Fatalf("进度应保留（轮1/1步），got 轮%d 步%d", restored.Round, len(restored.Steps))
	}
	if restored.SessionID != sA.UUID {
		t.Fatalf("恢复目标应归属会话 A，got %q", restored.SessionID)
	}
}

// TestSetMode_SessionIsolated 问题 2：模式按会话隔离——会话 A 切 goal 不影响
// 会话 B（B 未设置时回退全局默认）；切回 A 自动恢复 A 的模式。
func TestSetMode_SessionIsolated(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	sA := rt.NewSession("会话A")
	if rt.GetMode() != "chat" {
		t.Fatalf("默认模式应为 chat，got %s", rt.GetMode())
	}
	rt.SetMode("goal")
	if rt.GetMode() != "goal" {
		t.Fatalf("会话 A 切 goal 后 GetMode 应为 goal，got %s", rt.GetMode())
	}

	// 新建会话 B：不受 A 的 goal 影响（回退全局默认 chat）
	sB := rt.NewSession("会话B")
	if rt.GetMode() != "chat" {
		t.Fatalf("会话 B 不应继承会话 A 的 goal（问题 2），got %s", rt.GetMode())
	}
	rt.SetMode("task")
	if rt.GetMode() != "task" {
		t.Fatalf("会话 B 设 task 后应为 task，got %s", rt.GetMode())
	}

	// 切回 A：恢复 A 自己的模式 goal（不受 B 的 task 影响）
	if _, _, err := rt.LoadSession(sA.UUID); err != nil {
		t.Fatalf("LoadSession(A) 失败: %v", err)
	}
	if rt.GetMode() != "goal" {
		t.Fatalf("切回 A 应恢复 goal，got %s", rt.GetMode())
	}
	// 切到 B：仍为 task
	if _, _, err := rt.LoadSession(sB.UUID); err != nil {
		t.Fatalf("LoadSession(B) 失败: %v", err)
	}
	if rt.GetMode() != "task" {
		t.Fatalf("切到 B 应为 task，got %s", rt.GetMode())
	}
}

// TestSetMode_PersistModesAcrossRestart 模式按会话持久化：重启后恢复各会话自己的模式。
func TestSetMode_PersistModesAcrossRestart(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-mode-persist-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	rt1 := newTestRuntimeAt(t, tmpDir)
	sA := rt1.NewSession("会话A")
	rt1.SetMode("goal")

	// 模拟重启：新 Runtime 同目录（New → loadSettings 恢复 modes）
	rt2 := newTestRuntimeAt(t, tmpDir)
	if _, _, err := rt2.LoadSession(sA.UUID); err != nil {
		t.Fatalf("LoadSession(A) 失败: %v", err)
	}
	if rt2.GetMode() != "goal" {
		t.Fatalf("重启后会话 A 模式应恢复 goal，got %s", rt2.GetMode())
	}
	// 新会话仍用全局默认 chat
	sB := rt2.NewSession("会话B")
	if rt2.GetMode() != "chat" {
		t.Fatalf("重启后新会话 B 应回退全局默认 chat，got %s", rt2.GetMode())
	}
	_ = sB
}

// TestDeleteSession_CleansMode 删除会话同步清理其模式记录（防 map 泄漏）。
func TestDeleteSession_CleansMode(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	sA := rt.NewSession("会话A")
	rt.SetMode("goal")
	if err := rt.DeleteSession(sA.UUID); err != nil {
		t.Fatalf("DeleteSession 失败: %v", err)
	}
	rt.historyMu.RLock()
	_, ok := rt.modeBySID[sA.UUID]
	rt.historyMu.RUnlock()
	if ok {
		t.Fatal("删除会话后模式记录应被清理（防泄漏）")
	}
}
