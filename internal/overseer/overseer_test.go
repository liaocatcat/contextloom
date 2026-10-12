package overseer

import (
	"context"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestNew_DefaultRulesWhenNil(t *testing.T) {
	o := New("test goal", nil, types.Score(0.7), nil)

	if o.rules == nil {
		t.Fatal("expected default rules when nil is passed")
	}
	if o.rules.CompletenessWeight != 0.40 {
		t.Errorf("expected default CompletenessWeight=0.40, got %f", o.rules.CompletenessWeight)
	}
}

func TestNew_KeepsCustomRules(t *testing.T) {
	custom := &ScoringRules{
		CompletenessWeight: 0.50,
		TestCoverageWeight: 0.30,
		CodeQualityWeight:  0.15,
		NoSideEffectWeight: 0.05,
	}
	o := New("test goal", nil, types.Score(0.7), custom)

	if o.rules != custom {
		t.Fatal("expected custom rules to be retained")
	}
}

func TestNew_SetsProperties(t *testing.T) {
	goal := "build a web server"
	constraints := map[string]any{"min_test_coverage": 0.8, "min_code_quality": 0.7}
	threshold := types.Score(0.85)

	o := New(goal, constraints, threshold, nil)

	if o.Goal() != goal {
		t.Errorf("expected goal %q, got %q", goal, o.Goal())
	}
	if o.Constraints()["min_test_coverage"] != 0.8 {
		t.Errorf("expected min_test_coverage=0.8, got %v", o.Constraints()["min_test_coverage"])
	}
	if o.Constraints()["min_code_quality"] != 0.7 {
		t.Errorf("expected min_code_quality=0.7, got %v", o.Constraints()["min_code_quality"])
	}
}

func TestEvaluate_PassesWhenScoreAboveThreshold(t *testing.T) {
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	report := types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "all good",
	}

	passed := o.Evaluate(ctx, "agent-1", report)
	if !passed {
		t.Error("expected evaluation to pass with high scores")
	}
}

func TestEvaluate_FailsWhenScoreBelowThreshold(t *testing.T) {
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	report := types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.3,
		TestCoverage: 0.3,
		CodeQuality:  0.3,
		NoSideEffect: 0.3,
		Summary:      "poor quality",
	}

	passed := o.Evaluate(ctx, "agent-1", report)
	if passed {
		t.Error("expected evaluation to fail with low scores")
	}
}

func TestEvaluate_RecordsCheckpointOnPass(t *testing.T) {
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	report := types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "passed report",
	}

	o.Evaluate(ctx, "agent-pass", report)

	cps := o.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if !cps[0].Approved {
		t.Error("expected checkpoint to be approved")
	}
	if cps[0].ChildAgentID != "agent-pass" {
		t.Errorf("expected agent-pass, got %s", cps[0].ChildAgentID)
	}
	if cps[0].NodeName != "node-1" {
		t.Errorf("expected node-1, got %s", cps[0].NodeName)
	}
}

func TestEvaluate_RecordsCheckpointOnFail(t *testing.T) {
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	report := types.OverseerReport{
		NodeName:     "node-2",
		Completeness: 0.2,
		TestCoverage: 0.2,
		CodeQuality:  0.2,
		NoSideEffect: 0.2,
		Summary:      "failed report",
	}

	o.Evaluate(ctx, "agent-fail", report)

	cps := o.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if cps[0].Approved {
		t.Error("expected checkpoint to be rejected")
	}
	if cps[0].ChildAgentID != "agent-fail" {
		t.Errorf("expected agent-fail, got %s", cps[0].ChildAgentID)
	}
}

func TestEvaluate_ConstraintValidationCodeQuality(t *testing.T) {
	constraints := map[string]any{"min_code_quality": 0.8}
	o := New("test", constraints, types.Score(0.6), nil)
	ctx := context.Background()

	// Score passes threshold but code quality is below constraint minimum
	report := types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.5, // below min_code_quality
		NoSideEffect: 0.9,
		Summary:      "good except code quality",
	}

	passed := o.Evaluate(ctx, "agent-1", report)
	if passed {
		t.Error("expected evaluation to fail due to code_quality constraint")
	}

	cps := o.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if cps[0].Approved {
		t.Error("expected checkpoint to be rejected")
	}
	// Reason should indicate constraint validation failure
	if cps[0].Reason != "约束验证失败: good except code quality" {
		t.Errorf("expected constraint failure reason, got %q", cps[0].Reason)
	}
}

func TestEvaluate_ConstraintValidationTestCoverage(t *testing.T) {
	constraints := map[string]any{"min_test_coverage": 0.8}
	o := New("test", constraints, types.Score(0.6), nil)
	ctx := context.Background()

	report := types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.5, // below min_test_coverage
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "good except test coverage",
	}

	passed := o.Evaluate(ctx, "agent-1", report)
	if passed {
		t.Error("expected evaluation to fail due to test_coverage constraint")
	}
}

func TestCheckpoints_ReturnsCopy(t *testing.T) {
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "test",
	})

	// Get checkpoints and modify the returned slice
	cps := o.Checkpoints()
	cps[0] = types.OverseerCheckpoint{NodeName: "modified"}

	// Original should be unchanged
	originalCps := o.Checkpoints()
	if originalCps[0].NodeName != "node-1" {
		t.Error("expected Checkpoints() to return a copy, original was modified")
	}
}

func TestGoal_ReturnsGoal(t *testing.T) {
	goal := "implement user authentication"
	o := New(goal, nil, types.Score(0.7), nil)

	if o.Goal() != goal {
		t.Errorf("expected goal %q, got %q", goal, o.Goal())
	}
}

func TestConstraints_ReturnsNilWhenNil(t *testing.T) {
	o := New("test", nil, types.Score(0.7), nil)

	if o.Constraints() != nil {
		t.Errorf("expected nil constraints, got %v", o.Constraints())
	}
}

func TestEvaluate_MultipleCheckpointsAccumulate(t *testing.T) {
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "r1",
	})
	o.Evaluate(ctx, "agent-2", types.OverseerReport{
		NodeName:     "node-2",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "r2",
	})

	cps := o.Checkpoints()
	if len(cps) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(cps))
	}
}

// ---------------------------------------------------------------------------
// Anti-Cheat Integration Tests (Task 5: Evaluate integration)
// ---------------------------------------------------------------------------

func TestEvaluate_AntiCheatIntegration_TrustedAgentPasses(t *testing.T) {
	// When claimed and actual results match (VerifyExecution returns trusted),
	// and scores are above threshold, Evaluate should pass.
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	claimed := &types.ToolResult{Result: map[string]any{"passed": 5.0, "failed": 0.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 5.0, "failed": 0.0}}

	report := types.OverseerReport{
		NodeName:      "node-1",
		Completeness:  0.9,
		TestCoverage:  0.9,
		CodeQuality:   0.9,
		NoSideEffect:  0.9,
		Summary:       "clean agent",
		ClaimedResult: claimed,
		ActualResult:  actual,
	}

	passed := o.Evaluate(ctx, "agent-trusted", report)
	if !passed {
		t.Error("expected trusted agent to pass evaluation")
	}

	// Checkpoint should be approved
	cps := o.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if !cps[0].Approved {
		t.Error("expected checkpoint to be approved for trusted agent")
	}
}

func TestEvaluate_AntiCheatIntegration_CheatingDetectedFails(t *testing.T) {
	// When VerifyExecution detects cheating (similarity < 0.8),
	// Evaluate should fail even if weighted scores are above threshold.
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	// Claimed: all 10 passed; Actual: only 2 passed, 8 failed → clear cheating
	claimed := &types.ToolResult{Result: map[string]any{"passed": 10.0, "failed": 0.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 2.0, "failed": 8.0}}

	report := types.OverseerReport{
		NodeName:      "node-1",
		Completeness:  0.9,
		TestCoverage:  0.9,
		CodeQuality:   0.9,
		NoSideEffect:  0.9,
		Summary:       "agent claims success but actually failed",
		ClaimedResult: claimed,
		ActualResult:  actual,
	}

	passed := o.Evaluate(ctx, "agent-cheater", report)
	if passed {
		t.Error("expected cheating agent to fail evaluation")
	}

	// Checkpoint should be rejected with CHEATING reason
	cps := o.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(cps))
	}
	if cps[0].Approved {
		t.Error("expected checkpoint to be rejected for cheating agent")
	}
}

func TestEvaluate_AntiCheatIntegration_CheatingPurgesAgentCheckpoints(t *testing.T) {
	// When cheating is detected, previous checkpoints for that agent
	// should be purged and only the cheating rejection checkpoint remains.
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	// First: record a passing checkpoint for agent-1
	o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "initial pass",
	})

	// Now cheat with the same agent
	claimed := &types.ToolResult{Result: map[string]any{"passed": 10.0, "failed": 0.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 1.0, "failed": 9.0}}

	passed := o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:      "node-2",
		Completeness:  0.9,
		TestCoverage:  0.9,
		CodeQuality:   0.9,
		NoSideEffect:  0.9,
		Summary:       "cheating detected",
		ClaimedResult: claimed,
		ActualResult:  actual,
	})

	if passed {
		t.Error("expected cheating detection to fail evaluation")
	}

	// After cheating, agent-1's previous checkpoints should be purged
	// and only the cheating rejection should remain
	cps := o.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint after cheating purge, got %d", len(cps))
	}
	if cps[0].Approved {
		t.Error("expected remaining checkpoint to be rejected")
	}
	if cps[0].ChildAgentID != "agent-1" {
		t.Errorf("expected agent-1, got %s", cps[0].ChildAgentID)
	}
}

func TestEvaluate_AntiCheatIntegration_NoClaimedResultSkipsAntiCheat(t *testing.T) {
	// When ClaimedResult is nil, anti-cheat is skipped entirely (backward compatibility).
	// The evaluation should proceed based on scores alone.
	o := New("test", nil, types.Score(0.7), nil)
	ctx := context.Background()

	report := types.OverseerReport{
		NodeName:      "node-1",
		Completeness:  0.9,
		TestCoverage:  0.9,
		CodeQuality:   0.9,
		NoSideEffect:  0.9,
		Summary:       "no anti-cheat data",
		ClaimedResult: nil,
		ActualResult:  nil,
	}

	passed := o.Evaluate(ctx, "agent-no-claim", report)
	if !passed {
		t.Error("expected evaluation to pass when anti-cheat is skipped")
	}
}

func TestEvaluate_AntiCheatIntegration_CheatingOverridesConstraintPass(t *testing.T) {
	// Anti-cheat failure should take precedence over constraint validation pass.
	// Even if all constraints are met, CHEATING should make Evaluate return false.
	o := New("test", nil, types.Score(0.3), nil) // low threshold so score alone would pass
	ctx := context.Background()

	claimed := &types.ToolResult{Result: map[string]any{"passed": 10.0, "failed": 0.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 2.0, "failed": 8.0}}

	report := types.OverseerReport{
		NodeName:      "node-1",
		Completeness:  0.6,
		TestCoverage:  0.6,
		CodeQuality:   0.6,
		NoSideEffect:  0.6,
		Summary:       "scores above threshold but cheating",
		ClaimedResult: claimed,
		ActualResult:  actual,
	}

	passed := o.Evaluate(ctx, "agent-cheat-override", report)
	if passed {
		t.Error("expected cheating to override score-based pass")
	}
}
