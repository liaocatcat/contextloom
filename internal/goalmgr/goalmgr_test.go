package goalmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestStart_SessionInit(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	gs := m.Start("实现用户登录", 5, 100000)
	if gs == nil {
		t.Fatal("Start 返回 nil")
	}
	if gs.Phase != types.GoalPhasePlan {
		t.Fatalf("初始阶段应为 plan，got %s", gs.Phase)
	}
	if gs.MaxRounds != 5 {
		t.Fatalf("MaxRounds 应为 5，got %d", gs.MaxRounds)
	}
	if gs.Round != 0 {
		t.Fatalf("初始轮次应为 0，got %d", gs.Round)
	}
	if m.Session() != gs {
		t.Fatal("Session 应返回刚启动的会话")
	}
}

func TestStart_Defaults(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	gs := m.Start("目标", 0, 0)
	if gs.MaxRounds != DefaultMaxRounds {
		t.Fatalf("默认 MaxRounds 应为 %d，got %d", DefaultMaxRounds, gs.MaxRounds)
	}
	if gs.TokenBudget != 0 {
		t.Fatalf("默认 TokenBudget 应为 0（不限制），got %d", gs.TokenBudget)
	}
}

func TestApplyRoundResult_ModelDeclaresDone(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{
		{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone)},
	})

	// [GOAL_DONE] → 首次先进入打磨轮（v2：完成后再打磨一轮）
	cont, reason := m.ApplyRoundResult("完成 [GOAL_DONE]", true, 90, 100, nil)
	if !cont {
		t.Fatalf("模型声明 [GOAL_DONE] 后应先进入打磨轮继续，reason=%s", reason)
	}
	gs := m.Session()
	if gs.Phase != types.GoalPhaseCheck {
		t.Fatalf("阶段应为 check（打磨轮），got %s", gs.Phase)
	}
	if gs.Round != 1 {
		t.Fatalf("轮次应为 1，got %d", gs.Round)
	}
	if gs.TokenUsed != 100 {
		t.Fatalf("token 应累计 100，got %d", gs.TokenUsed)
	}
	if len(gs.Checkpoints) != 1 {
		t.Fatalf("应有 1 个检查点，got %d", len(gs.Checkpoints))
	}
	if !strings.Contains(gs.Continuation, "打磨轮") {
		t.Fatalf("打磨轮 continuation 应含打磨指令，got %q", gs.Continuation)
	}
	// 打磨轮完成 → 真正 done
	cont, reason = m.ApplyRoundResult("打磨完成 [GOAL_POLISH_DONE]", true, 92, 50, nil)
	if cont {
		t.Fatalf("打磨轮完成后不应继续，reason=%s", reason)
	}
	gs = m.Session()
	if gs.Phase != types.GoalPhaseDone {
		t.Fatalf("阶段应为 done，got %s", gs.Phase)
	}
	if !gs.Polished {
		t.Fatal("Polished 标记应为 true")
	}
}

func TestApplyRoundResult_AllStepsDoneAndPassed(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{
		{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone), Evidence: "测试通过"},
		{ID: "s2", Desc: "步骤2", Status: string(types.GoalStepDone), Evidence: "文档已更新"},
	})

	// 全部步骤有效 done（evidence 非空）+ 通过 → 先进入打磨轮
	cont, reason := m.ApplyRoundResult("全部完成", true, 95, 50, nil)
	if !cont {
		t.Fatalf("全部步骤 done 且通过后应先进入打磨轮，reason=%s", reason)
	}
	gs := m.Session()
	if gs.Phase != types.GoalPhaseCheck {
		t.Fatalf("阶段应为 check（打磨轮），got %s", gs.Phase)
	}
	// 打磨轮完成 → 真正 done
	cont, reason = m.ApplyRoundResult("打磨完成 [GOAL_POLISH_DONE]", true, 95, 50, nil)
	if cont {
		t.Fatalf("打磨轮完成后不应继续，reason=%s", reason)
	}
	gs = m.Session()
	if gs.Phase != types.GoalPhaseDone {
		t.Fatalf("阶段应为 done，got %s", gs.Phase)
	}
	if !strings.Contains(reason, "打磨轮通过") {
		t.Fatalf("reason 应包含打磨轮通过说明，got %q", reason)
	}
}

func TestApplyRoundResult_ContinueWithContinuation(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{
		{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)},
		{ID: "s2", Desc: "步骤2", Status: string(types.GoalStepPending)},
	})

	// 模型汇报 s1 done、s2 未动
	steps := []types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone)}}
	cont, reason := m.ApplyRoundResult("完成了 s1", true, 80, 200, steps)
	if !cont {
		t.Fatalf("未全部完成应继续，reason=%s", reason)
	}
	gs := m.Session()
	if gs.Phase != types.GoalPhaseExecute {
		t.Fatalf("阶段应为 execute，got %s", gs.Phase)
	}
	if gs.Round != 1 {
		t.Fatalf("轮次应为 1，got %d", gs.Round)
	}
	if gs.Continuation == "" {
		t.Fatal("应生成 continuation")
	}
	if !strings.Contains(gs.Continuation, "s2") {
		t.Fatalf("continuation 应提及未完成步骤 s2，got %q", gs.Continuation)
	}
	// s1 状态被合并更新
	found := false
	for _, st := range gs.Steps {
		if st.ID == "s1" && st.Status == string(types.GoalStepDone) {
			found = true
		}
	}
	if !found {
		t.Fatal("模型回报的 s1=done 应合并进步骤列表")
	}
}

func TestApplyRoundResult_MaxRoundsExceeded(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 2, 0) // 建议 2 轮
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})

	cont, _ := m.ApplyRoundResult("进展", true, 70, 10, nil)
	if !cont {
		t.Fatal("第 1 轮应继续")
	}
	// 达到建议轮次上限后：软提示继续，不强制 failed
	cont, reason := m.ApplyRoundResult("进展", true, 70, 10, nil)
	if !cont {
		t.Fatalf("达到建议轮次后应继续执行（软提示），reason=%s", reason)
	}
	gs := m.Session()
	if gs.Phase == types.GoalPhaseFailed {
		t.Fatal("达到建议轮次不应置为 failed（轮次上限已改为软提示）")
	}
	if !strings.Contains(gs.Continuation, "轮次提示") {
		t.Fatalf("continuation 应含轮次提示，got %q", gs.Continuation)
	}
	if !strings.Contains(gs.Continuation, "GOAL_DONE") {
		t.Fatalf("continuation 应提示可输出 [GOAL_DONE] 收敛，got %q", gs.Continuation)
	}
}

func TestApplyRoundResult_TokenBudgetExceeded(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 150) // token 预算 150
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})

	cont, _ := m.ApplyRoundResult("第一轮", true, 70, 100, nil)
	if !cont {
		t.Fatal("第 1 轮（100/150）应继续")
	}
	cont, reason := m.ApplyRoundResult("第二轮", true, 70, 100, nil) // 累计 200 > 150
	if cont {
		t.Fatalf("超 token 预算后不应继续，reason=%s", reason)
	}
	gs := m.Session()
	if gs.Phase != types.GoalPhaseFailed {
		t.Fatalf("阶段应为 failed，got %s", gs.Phase)
	}
	if !strings.Contains(reason, "token 预算") {
		t.Fatalf("reason 应说明 token 预算，got %q", reason)
	}
}

func TestApplyRoundResult_NotPassedRetries(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 5, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone)}})

	// 步骤全 done 但监工未通过 → 继续（要求复查质量）
	cont, reason := m.ApplyRoundResult("完成了", false, 55, 50, nil)
	if !cont {
		t.Fatalf("步骤全 done 但未通过时应继续重试，reason=%s", reason)
	}
	if !strings.Contains(m.Session().Continuation, "未通过") {
		t.Fatalf("continuation 应说明未通过，got %q", m.Session().Continuation)
	}
}

func TestAbort(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 5, 0)
	m.Abort()
	gs := m.Session()
	if !gs.Aborted || !gs.Done() {
		t.Fatalf("Abort 后应标记 aborted，got %+v", gs)
	}
}

func TestReset(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 5, 0)
	m.Reset()
	if m.Session() != nil {
		t.Fatal("Reset 后 Session 应为 nil")
	}
}

func TestPersistAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goals")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	gs := m.Start("持久化目标", 5, 1000)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})
	cont, _ := m.ApplyRoundResult("进展中", false, 60, 100, nil)
	if !cont {
		t.Fatal("应继续")
	}
	// 持久化文件应存在
	if _, err := os.Stat(filepath.Join(dir, gs.ID+".json")); err != nil {
		t.Fatalf("持久化文件不存在: %v", err)
	}

	// 新 Manager 加载
	m2 := New(dir)
	loaded, err := m2.Load(gs.ID)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if loaded.Goal != "持久化目标" {
		t.Fatalf("Goal 恢复不符: %q", loaded.Goal)
	}
	if loaded.Round != 1 || len(loaded.Steps) != 1 || len(loaded.Checkpoints) != 1 {
		t.Fatalf("会话状态恢复不符: round=%d steps=%d cps=%d", loaded.Round, len(loaded.Steps), len(loaded.Checkpoints))
	}
	if loaded.Continuation == "" {
		t.Fatal("continuation 应恢复")
	}

	// List 应包含
	list := m2.List()
	if len(list) != 1 {
		t.Fatalf("List 应有 1 个会话，got %d", len(list))
	}
	if list[0].ID != gs.ID {
		t.Fatalf("List 首个应为最新会话 %s，got %s", gs.ID, list[0].ID)
	}
}

func TestLoad_NotFound(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	if _, err := m.Load("goal-nonexistent"); err == nil {
		t.Fatal("加载不存在的会话应报错")
	}
}

func TestCheckpointCap(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 30, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})
	for i := 0; i < 25; i++ {
		m.ApplyRoundResult("轮次", true, 70, 10, nil)
	}
	gs := m.Session()
	if len(gs.Checkpoints) > 20 {
		t.Fatalf("检查点应封顶 20 个，got %d", len(gs.Checkpoints))
	}
	if gs.Round != 25 {
		t.Fatalf("轮次应为 25，got %d", gs.Round)
	}
}

// ── 多会话隔离测试（goal 状态机按对话会话隔离，修复"新建会话串目标"）──

func TestManager_SessionIsolation(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))

	// 会话 A：建立目标 A
	m.SetCurrent("sess-A")
	gsA := m.Start("目标A", 5, 0)
	if gsA.SessionID != "sess-A" {
		t.Fatalf("目标A 应归属 sess-A，got %q", gsA.SessionID)
	}
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤A1", Status: string(types.GoalStepPending)}})

	// 切到会话 B：不应看到 A 的目标
	m.SetCurrent("sess-B")
	if m.Session() != nil {
		t.Fatal("会话 B 不应看到会话 A 的目标（串扰）")
	}
	gsB := m.Start("目标B", 5, 0)
	if gsB.SessionID != "sess-B" {
		t.Fatalf("目标B 应归属 sess-B，got %q", gsB.SessionID)
	}
	if m.Session().Goal != "目标B" {
		t.Fatalf("会话 B 应持有自己的目标，got %q", m.Session().Goal)
	}
	// B 推进不影响 A
	m.ApplyRoundResult("B 的进展", true, 80, 100, nil)
	if m.Session().Round != 1 {
		t.Fatalf("B 目标轮次应为 1，got %d", m.Session().Round)
	}

	// 切回会话 A：恢复 A 自己的目标与进度（不丢）
	m.SetCurrent("sess-A")
	backA := m.Session()
	if backA == nil || backA.Goal != "目标A" {
		t.Fatalf("切回 A 应恢复目标A，got %+v", backA)
	}
	if backA.Round != 0 || len(backA.Steps) != 1 {
		t.Fatalf("A 目标进度应保留（轮0/1步），got 轮%d 步%d", backA.Round, len(backA.Steps))
	}

	// Reset 只清当前会话：清 A 不影响 B
	m.Reset()
	if m.Session() != nil {
		t.Fatal("A 的 Reset 后 A 目标应为 nil")
	}
	m.SetCurrent("sess-B")
	if m.Session() == nil || m.Session().Goal != "目标B" {
		t.Fatal("Reset A 不应影响会话 B 的目标")
	}
}

func TestManager_SessionIsolation_PersistLoad(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.SetCurrent("sess-A")
	gsA := m.Start("持久化隔离目标", 5, 0)

	// 模拟 A 切走后 B 也建了目标
	m.SetCurrent("sess-B")
	m.Start("B目标", 5, 0)

	// 缺陷 B 修复：A 的目标不能在会话 B 跨会话 resume（防止改绑"串"）
	if _, err := m.Load(gsA.ID); err == nil {
		t.Fatal("跨会话 resume 应被拒绝（归属校验），防止目标被改绑到 B")
	}
	if m.Session().ID != "" && m.Session().Goal != "B目标" {
		t.Fatalf("B 会话目标不应被跨会话加载干扰，got %+v", m.Session())
	}

	// 同会话 resume：切回 A 后恢复 A 自己的目标（隔离 + 进度保留）
	m.SetCurrent("sess-A")
	loaded, err := m.Load(gsA.ID)
	if err != nil {
		t.Fatalf("同会话 Load 失败: %v", err)
	}
	if loaded.SessionID != "sess-A" {
		t.Fatalf("resume 目标应归属会话 A，got %q", loaded.SessionID)
	}
	if m.Session().ID != gsA.ID {
		t.Fatalf("A 会话当前目标应为 resume 的目标，got %s", m.Session().ID)
	}
}

// ── v2 自进化机制测试（反思/缺陷/收敛/经验/DoD）──

func TestApplyRoundResultEx_ReflectionRecorded(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})

	refl := types.GoalReflection{Summary: "完成了 s1 的分析", Problems: "发现 s2 依赖外部库", NextPlan: "下一步实现 s2"}
	cont, _ := m.ApplyRoundResultEx("进展", true, 80, 100, nil, refl, nil, nil, nil)
	if !cont {
		t.Fatal("应继续")
	}
	gs := m.Session()
	if len(gs.Reflections) != 1 {
		t.Fatalf("应有 1 条反思，got %d", len(gs.Reflections))
	}
	r := gs.Reflections[0]
	if r.Round != 1 || r.Summary != "完成了 s1 的分析" || r.Problems == "" || r.NextPlan == "" {
		t.Fatalf("反思内容异常: %+v", r)
	}
	// continuation 应包含反思摘要（下一轮开局先读）
	if !strings.Contains(gs.Continuation, "上轮反思") || !strings.Contains(gs.Continuation, "下一步: 下一步实现 s2") {
		t.Fatalf("continuation 应含反思摘要，got %q", gs.Continuation)
	}
	// 反思上限 20
	for i := 0; i < 25; i++ {
		m.ApplyRoundResultEx("继续", true, 80, 10, nil, refl, nil, nil, nil)
	}
	if len(m.Session().Reflections) > 20 {
		t.Fatalf("反思应限 20 条，got %d", len(m.Session().Reflections))
	}
}

func TestApplyRoundResultEx_DefectFlow(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})

	// 未通过 + 监工低分维度缺陷
	auto := []types.GoalDefect{
		{ID: "d-test", Desc: "测试覆盖不足", Status: "open"},
		{ID: "d-quality", Desc: "代码质量不足", Status: "open"},
	}
	cont, reason := m.ApplyRoundResultEx("写完了", false, 60, 100, nil, types.GoalReflection{}, auto, nil, nil)
	if !cont {
		t.Fatalf("未通过应继续返工，reason=%s", reason)
	}
	gs := m.Session()
	if len(gs.OpenDefects()) != 2 {
		t.Fatalf("应有 2 个 open 缺陷，got %d", len(gs.OpenDefects()))
	}
	// continuation 应列出缺陷清单
	if !strings.Contains(gs.Continuation, "缺陷清单") || !strings.Contains(gs.Continuation, "d-test") {
		t.Fatalf("continuation 应含缺陷清单，got %q", gs.Continuation)
	}
	// 模型回报修复 d-test
	fix := []types.GoalDefect{{ID: "d-test", Status: "fixed", Evidence: "新增 3 个测试全过"}}
	m.ApplyRoundResultEx("补了测试", true, 85, 100, nil, types.GoalReflection{}, nil, fix, nil)
	gs = m.Session()
	if len(gs.OpenDefects()) != 1 {
		t.Fatalf("修复后应剩 1 个 open 缺陷，got %d", len(gs.OpenDefects()))
	}
	for _, d := range gs.Defects {
		if d.ID == "d-test" {
			if d.Status != "fixed" || d.FixedRound != 2 || d.Evidence == "" {
				t.Fatalf("缺陷修复状态异常: %+v", d)
			}
		}
	}
	// 同 desc 缺陷去重（复发重新 open）
	m.ApplyRoundResultEx("又没测试", false, 55, 100, nil, types.GoalReflection{}, auto, nil, nil)
	gs = m.Session()
	n := 0
	for _, d := range gs.Defects {
		if d.Desc == "测试覆盖不足" {
			n++
		}
	}
	if n != 1 || gs.OpenDefects()[0].ID != "d-test" {
		t.Fatalf("缺陷应去重且复发重新 open，got n=%d open=%v", n, gs.OpenDefects())
	}
}

func TestApplyRoundResultEx_StallDetection(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})

	// 连续 4 轮无变化（同步骤状态 + 同分；首轮因 prevScore=0→80 计为进展）
	for i := 0; i < 4; i++ {
		m.ApplyRoundResultEx("重复工作", true, 80, 100, nil, types.GoalReflection{}, nil, nil, nil)
	}
	gs := m.Session()
	if gs.StalledRounds != 3 {
		t.Fatalf("连续无进展应累计 3，got %d", gs.StalledRounds)
	}
	// 第 2 轮起 continuation 应含无进展告警
	if !strings.Contains(gs.Continuation, "无进展告警") {
		t.Fatalf("停滞>=2 轮 continuation 应含无进展告警，got %q", gs.Continuation)
	}
	// 有实质进展（步骤 done + 分数变化）→ 清零
	steps := []types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone), Evidence: "测试通过"}}
	m.ApplyRoundResultEx("终于完成了", true, 92, 100, steps, types.GoalReflection{}, nil, nil, nil)
	gs = m.Session()
	if gs.StalledRounds != 0 || gs.LastProgressRnd != 5 {
		t.Fatalf("进展后停滞应清零，got stalled=%d last=%d", gs.StalledRounds, gs.LastProgressRnd)
	}
}

func TestApplyRoundResultEx_Lessons(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})

	m.ApplyRoundResultEx("进展", true, 80, 100, nil, types.GoalReflection{}, nil, nil, []string{"上次超时是因为并行子任务太多"})
	gs := m.Session()
	if len(gs.Lessons) != 1 || gs.Lessons[0] != "上次超时是因为并行子任务太多" {
		t.Fatalf("经验应记录，got %v", gs.Lessons)
	}
	// continuation 应含经验档案（下一轮开局先读）
	if !strings.Contains(gs.Continuation, "经验档案") {
		t.Fatalf("continuation 应含经验档案，got %q", gs.Continuation)
	}
	// 去重 + 上限 10
	for i := 0; i < 15; i++ {
		m.ApplyRoundResultEx("继续", true, 80, 10, nil, types.GoalReflection{}, nil, nil, []string{fmt.Sprintf("教训%d", i)})
	}
	gs = m.Session()
	if len(gs.Lessons) > 10 {
		t.Fatalf("经验应限 10 条，got %d", len(gs.Lessons))
	}
	if gs.Lessons[0] != "教训14" {
		t.Fatalf("最新经验应在最前，got %v", gs.Lessons[0])
	}
}

func TestApplyRoundResultEx_DoDRequiresEvidence(t *testing.T) {
	m := New(filepath.Join(t.TempDir(), "goals"))
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone)}})

	// 步骤 done 但 evidence 为空 → 非"有效 done" → 不触发打磨轮，继续执行
	cont, _ := m.ApplyRoundResult("全部完成", true, 95, 50, nil)
	if !cont {
		t.Fatal("无 evidence 的 done 不应触发完成/打磨，应继续")
	}
	gs := m.Session()
	if gs.Phase == types.GoalPhaseDone || gs.Phase == types.GoalPhaseCheck {
		t.Fatalf("无证据不应进入完成路径，phase=%s", gs.Phase)
	}
}

// ── 缺陷 A/B 回归：重启自动恢复目标 + resume 归属校验 ──

// TestRestoreFor_AfterRestart 缺陷 A：backend 重启后 RestoreFor 应自动把
// 该会话最近未完成的目标挂回内存（此前 bySID 全空 → 发消息静默新建目标、旧目标失联）。
func TestRestoreFor_AfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goals")
	m1 := New(dir)
	m1.SetCurrent("sess-A")
	gsA := m1.Start("重启恢复目标", 5, 0)
	m1.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepPending)}})
	m1.ApplyRoundResult("进展中", false, 60, 100, nil) // round=1 未完成

	// 模拟 backend 重启：新 Manager 同目录，bySID 全空
	m2 := New(dir)
	m2.SetCurrent("sess-A")
	restored := m2.RestoreFor("sess-A")
	if restored == nil {
		t.Fatal("RestoreFor 应恢复会话 A 的未完成目标")
	}
	if restored.ID != gsA.ID {
		t.Fatalf("应恢复目标 %s，got %s", gsA.ID, restored.ID)
	}
	if restored.Round != 1 || len(restored.Steps) != 1 {
		t.Fatalf("进度应保留（轮1/1步），got 轮%d 步%d", restored.Round, len(restored.Steps))
	}
	// 已挂回内存：Session() 可见
	if m2.Session() == nil || m2.Session().ID != gsA.ID {
		t.Fatal("RestoreFor 后 Session() 应返回恢复的目标")
	}
}

// TestRestoreFor_CompletedNotRestored 已完成的目标不自动恢复（避免旧目标顶替新目标）。
func TestRestoreFor_CompletedNotRestored(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goals")
	m1 := New(dir)
	m1.SetCurrent("sess-A")
	m1.Start("已完成目标", 5, 0)
	m1.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: string(types.GoalStepDone)}})
	m1.ApplyRoundResult("完成 [GOAL_DONE]", true, 90, 100, nil)         // → 打磨轮
	m1.ApplyRoundResult("打磨完成 [GOAL_POLISH_DONE]", true, 92, 50, nil) // → done
	if !m1.Session().Done() {
		t.Fatal("前置：目标应已完成")
	}

	m2 := New(dir)
	m2.SetCurrent("sess-A")
	if r := m2.RestoreFor("sess-A"); r != nil {
		t.Fatalf("已完成目标不应自动恢复，got %s", r.ID)
	}
	if m2.Session() != nil {
		t.Fatal("已完成目标不应挂回内存")
	}
}

// TestRestoreFor_NoMatch 无匹配会话/无目标目录时返回 nil 不 panic。
func TestRestoreFor_NoMatch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goals")
	m1 := New(dir)
	m1.SetCurrent("sess-A")
	m1.Start("目标A", 5, 0)
	m1.SetCurrent("sess-B")
	m1.Start("目标B", 5, 0)

	m2 := New(dir)
	m2.SetCurrent("sess-X") // 无任何目标归属 X
	if r := m2.RestoreFor("sess-X"); r != nil {
		t.Fatalf("无匹配会话不应恢复，got %s", r.ID)
	}
	m3 := New(filepath.Join(t.TempDir(), "empty")) // 目录不存在
	if r := m3.RestoreFor("sess-A"); r != nil {
		t.Fatalf("无目标目录不应恢复，got %s", r.ID)
	}
}

// TestLoad_OwnershipMismatchRejected 缺陷 B：目标归属其他会话时 resume 必须拒绝，
// 防止把 A 的目标改绑到 B（"串"的另一半来源）。
func TestLoad_OwnershipMismatchRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goals")
	m := New(dir)
	m.SetCurrent("sess-A")
	gsA := m.Start("归属A的目标", 5, 0)

	m.SetCurrent("sess-B")
	if _, err := m.Load(gsA.ID); err == nil {
		t.Fatal("B 会话 resume A 的目标应被拒绝")
	}
	// 磁盘归属不被改写
	m.SetCurrent("sess-A")
	loaded, err := m.Load(gsA.ID)
	if err != nil {
		t.Fatalf("A 会话自己 resume 应成功: %v", err)
	}
	if loaded.SessionID != "sess-A" {
		t.Fatalf("目标归属应保持 sess-A，got %q", loaded.SessionID)
	}
}

// TestLoad_EmptyOwnershipBinds 旧数据（SessionID 为空/未绑定）兼容绑定到当前会话。
func TestLoad_EmptyOwnershipBinds(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "goals")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	m := New(dir)
	// 直接写一个无归属的旧格式目标文件
	old := &types.GoalSession{
		ID:    "goal-legacy-1",
		Goal:  "旧格式目标",
		Phase: types.GoalPhasePlan,
	}
	data, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, old.ID+".json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	m.SetCurrent("sess-A")
	loaded, err := m.Load("goal-legacy-1")
	if err != nil {
		t.Fatalf("无归属旧数据应可加载: %v", err)
	}
	if loaded.SessionID != "sess-A" {
		t.Fatalf("无归属旧数据应绑定当前会话，got %q", loaded.SessionID)
	}
}
