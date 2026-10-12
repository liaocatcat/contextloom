package runtime

import (
	"context"
	"os"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestGoal_SpecRefinementLoop 需求细化（spec 阶段）确定性门槛的端到端验证：
// R1 模型输出不合格的 [GOAL_SPEC]（无目标物/无假设/验收不可测）→ 状态机拒绝、
// 产生 d-spec 缺陷、不允许进入 plan；R2 补齐后通过 → 进入 plan。
func TestGoal_SpecRefinementLoop(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.SetMode("goal")
	rt.Backend = &scriptedBackend{responses: []string{
		// R1：不合格 spec（空泛细化，无目标物指代、无假设、验收不可测）
		"我来细化一下 [GOAL_SPEC]\n{\"goal_refined\":\"把登录做得好用一些\",\"assumptions\":[],\"acceptance\":[\"用户体验好\"]}\n[/GOAL_SPEC]",
		// R2：合格 spec
		"[GOAL_SPEC]\n{\"goal_refined\":\"实现 internal/web/login.go 登录页：表单+校验+接口对接\",\"assumptions\":[\"沿用 gin 路由\",\"POST /login 已存在\"],\"acceptance\":[\"go test ./internal/web 通过（0 失败）\",\"登录页含 3 个表单字段\"]}\n[/GOAL_SPEC]",
	}}

	ctx := context.Background()
	ch, errCh, err := rt.ProcessInput(ctx, "加个登录页")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	r1 := collectStream(t, ch, errCh)
	passed1, score1, rep1 := rt.OverseerEvaluate(r1)
	cont, reason := rt.GoalOnRoundEnd(r1, passed1, score1, rep1)
	if !cont {
		t.Fatalf("R1 细化被拒后应继续下一轮细化，reason=%s", reason)
	}
	gs := rt.GoalSession()
	if gs.SpecReady {
		t.Fatal("R1 不合格 spec 不应 SpecReady")
	}
	if gs.ActivePhase() != types.GoalPhaseSpec {
		t.Fatalf("拒绝后应停留 spec 阶段，got %s", gs.ActivePhase())
	}
	if len(gs.OpenDefects()) != 1 || gs.OpenDefects()[0].ID != "d-spec" {
		t.Fatalf("应有 d-spec 缺陷，got %+v", gs.OpenDefects())
	}
	if len(gs.Steps) != 0 {
		t.Fatalf("spec 未过不应进入 plan（步骤应为空），got %d", len(gs.Steps))
	}

	// R2：合格 spec 通过
	ch, errCh, err = rt.ProcessGoalRound(ctx)
	if err != nil {
		t.Fatalf("ProcessGoalRound R2: %v", err)
	}
	r2 := collectStream(t, ch, errCh)
	passed2, score2, rep2 := rt.OverseerEvaluate(r2)
	cont, reason = rt.GoalOnRoundEnd(r2, passed2, score2, rep2)
	if !cont {
		t.Fatalf("R2 spec 通过后应继续（进入 plan 起草步骤），reason=%s", reason)
	}
	gs = rt.GoalSession()
	if !gs.SpecReady {
		t.Fatal("R2 合格 spec 应 SpecReady")
	}
	// 状态机视角：spec 通过后 Phase 推进（plan→execute 由轮末收口推进；
	// 步骤清单在后续轮由 [GOAL_PLAN] 提取——此处 R2 无 plan 输出，故无步骤）。
	if gs.Phase == types.GoalPhaseSpec {
		t.Fatalf("spec 通过后不应停留 spec 阶段，got %s", gs.Phase)
	}
	if len(gs.Steps) != 0 {
		t.Fatalf("R2 未输出 plan，步骤应为空，got %d", len(gs.Steps))
	}
	// d-spec 缺陷应被划掉（SpecReady 后该缺陷不应再 open）
	for _, d := range gs.OpenDefects() {
		if d.ID == "d-spec" {
			t.Fatalf("spec 通过后 d-spec 缺陷不应再 open，got %+v", gs.OpenDefects())
		}
	}
}
