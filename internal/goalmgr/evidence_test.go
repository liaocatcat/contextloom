package goalmgr

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 证据链：VerifyGoalProofs 三类物理通道 ──

func TestVerifyGoalProofs_File(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.go")
	os.WriteFile(real, []byte("package x"), 0644)

	steps := []types.GoalStep{
		{ID: "s1", Status: "done", Evidence: "创建 real.go",
			Proofs: []types.GoalProof{{Kind: "file", Target: "real.go"}}},
		{ID: "s2", Status: "done", Evidence: "创建 missing.go",
			Proofs: []types.GoalProof{{Kind: "file", Target: "missing.go"}}},
	}
	fail := VerifyGoalProofs(steps, ProofIO{ProjectRoot: dir, StatFile: os.Stat})
	if len(fail) != 1 || fail[0].StepID != "s2" {
		t.Fatalf("应只有 s2 失败，got %+v", fail)
	}
}

func TestVerifyGoalProofs_Diff(t *testing.T) {
	steps := []types.GoalStep{
		{ID: "s1", Status: "done", Evidence: "改了 a.go",
			Proofs: []types.GoalProof{{Kind: "diff", Target: "a.go"}}},
		{ID: "s2", Status: "done", Evidence: "改了不存在 b.go",
			Proofs: []types.GoalProof{{Kind: "diff", Target: "b.go"}}},
	}
	io := ProofIO{
		ProjectRoot: "/proj",
		StatFile:    os.Stat,
		DiffNames:   func(root string) ([]string, error) { return []string{"a.go", "c.go"}, nil },
	}
	fail := VerifyGoalProofs(steps, io)
	if len(fail) != 1 || fail[0].StepID != "s2" {
		t.Fatalf("应只有 s2 失败，got %+v", fail)
	}
}

func TestVerifyGoalProofs_Test(t *testing.T) {
	steps := []types.GoalStep{
		{ID: "s1", Status: "done", Evidence: "测试通过",
			Proofs: []types.GoalProof{{Kind: "test", Target: "./pkg"}}},
		{ID: "s2", Status: "done", Evidence: "测试通过但实际失败",
			Proofs: []types.GoalProof{{Kind: "test", Target: "./bad"}}},
		{ID: "s3", Status: "done", Evidence: "未知证据类型",
			Proofs: []types.GoalProof{{Kind: "magic", Target: "x"}}},
	}
	io := ProofIO{
		ProjectRoot: "/proj",
		StatFile:    os.Stat,
		DiffNames:   func(string) ([]string, error) { return nil, nil },
		RunTest: func(root, target string, timeout time.Duration) (string, bool, error) {
			if target == "./bad" {
				return "FAIL", false, nil
			}
			return "ok", true, nil
		},
	}
	fail := VerifyGoalProofs(steps, io)
	if len(fail) != 2 {
		t.Fatalf("应有 2 个失败（bad + magic），got %+v", fail)
	}
	ids := map[string]bool{}
	for _, f := range fail {
		ids[f.StepID] = true
	}
	if !ids["s2"] || !ids["s3"] {
		t.Fatalf("应失败 s2/s3，got %+v", fail)
	}
}

func TestVerifyGoalProofs_NoProofsCompat(t *testing.T) {
	// 步骤 done 但无 proofs → 兼容放行（旧逻辑）
	steps := []types.GoalStep{{ID: "s1", Status: "done", Evidence: "完成"}}
	if fail := VerifyGoalProofs(steps, DefaultProofIO("/tmp")); len(fail) != 0 {
		t.Fatalf("无 proofs 步骤不应被核验，got %+v", fail)
	}
}

// ── 状态机接线：证据失败 → 降级 + 缺陷 ──

func TestApplyRoundResultEx_EvidenceFailure(t *testing.T) {
	m := New(t.TempDir())
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: "pending"}})
	// 注入证据核验器：s1 的 file 证据恒失败
	m.SetEvidenceVerifier(func(steps []types.GoalStep) []ProofFailure {
		return []ProofFailure{{StepID: "s1", Kind: "file", Target: "x.go", Reason: "文件不存在"}}
	})

	steps := []types.GoalStep{
		{ID: "s1", Desc: "步骤1", Status: "done", Evidence: "完成",
			Proofs: []types.GoalProof{{Kind: "file", Target: "x.go"}}},
	}
	m.ApplyRoundResultEx("完成了", true, 95, 50, steps, types.GoalReflection{}, nil, nil, nil)
	gs := m.Session()
	if gs.Steps[0].Status != "pending" {
		t.Fatalf("证据失败后步骤应降级 pending，got %s", gs.Steps[0].Status)
	}
	found := false
	for _, d := range gs.OpenDefects() {
		if d.ID == "d-evidence-s1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有 d-evidence-s1 缺陷，got %+v", gs.OpenDefects())
	}
}

// ── 打磨轮外部验收硬化 ──

func TestApplyRoundResultEx_PolishExternalVerifyFail(t *testing.T) {
	m := New(t.TempDir())
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: "done", Evidence: "完成"}})
	// 注入打磨验收器：永远失败
	m.SetPolishVerifier(func(s *types.GoalSession) (bool, []string) {
		return false, []string{"go test ./... 失败"}
	})

	// 首次 [GOAL_DONE] → 进入打磨轮
	m.ApplyRoundResult("完成 [GOAL_DONE]", true, 95, 50, nil)
	if m.Session().Phase != types.GoalPhaseCheck {
		t.Fatalf("应先进入打磨轮，got %s", m.Session().Phase)
	}
	// 打磨轮输出 [GOAL_POLISH_DONE] 但外部验收失败 → 拒绝，继续修复
	cont, reason := m.ApplyRoundResult("打磨完成 [GOAL_POLISH_DONE]", true, 95, 50, nil)
	if !cont {
		t.Fatalf("外部验收失败应继续修复，reason=%s", reason)
	}
	gs := m.Session()
	if gs.Done() {
		t.Fatal("外部验收失败不应 done")
	}
	found := false
	for _, d := range gs.OpenDefects() {
		if d.ID == "d-polish" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有 d-polish 缺陷，got %+v", gs.OpenDefects())
	}
}

func TestApplyRoundResultEx_PolishExternalVerifyPass(t *testing.T) {
	m := New(t.TempDir())
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: "done", Evidence: "完成"}})
	m.SetPolishVerifier(func(s *types.GoalSession) (bool, []string) { return true, nil })

	m.ApplyRoundResult("完成 [GOAL_DONE]", true, 95, 50, nil)
	cont, reason := m.ApplyRoundResult("打磨完成 [GOAL_POLISH_DONE]", true, 95, 50, nil)
	if cont {
		t.Fatalf("外部验收通过应完成，reason=%s", reason)
	}
	if !m.Session().Done() || !m.Session().Polished {
		t.Fatal("应 done + polished")
	}
}

// TestApplyRoundResultEx_PolishNoVerifierCompat 未注入验收器时走旧行为
func TestApplyRoundResultEx_PolishNoVerifierCompat(t *testing.T) {
	m := New(t.TempDir())
	m.Start("目标", 10, 0)
	m.SetPlan([]types.GoalStep{{ID: "s1", Desc: "步骤1", Status: "done", Evidence: "完成"}})
	m.ApplyRoundResult("完成 [GOAL_DONE]", true, 95, 50, nil)
	cont, _ := m.ApplyRoundResult("打磨完成 [GOAL_POLISH_DONE]", true, 95, 50, nil)
	if cont {
		t.Fatal("未注入验收器应兼容旧行为（标记即完成）")
	}
	if !m.Session().Done() {
		t.Fatal("应 done")
	}
}
