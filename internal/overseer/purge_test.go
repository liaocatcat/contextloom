package overseer

import (
	"context"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestOverseer_Purge_ClearsAllCheckpoints(t *testing.T) {
	o := New("test goal", map[string]any{"min_test_coverage": 0.5}, types.Score(70), nil)

	// Add some checkpoints via Evaluate
	ctx := context.Background()
	o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.8,
		CodeQuality:  0.7,
		NoSideEffect: 0.9,
		Summary:      "first report",
	})
	o.Evaluate(ctx, "agent-2", types.OverseerReport{
		NodeName:     "node-2",
		Completeness: 0.5,
		TestCoverage: 0.4,
		CodeQuality:  0.3,
		NoSideEffect: 0.2,
		Summary:      "second report",
	})

	// Verify checkpoints exist before purge
	if len(o.Checkpoints()) != 2 {
		t.Fatalf("expected 2 checkpoints before purge, got %d", len(o.Checkpoints()))
	}

	// Purge
	o.Purge()

	// Verify all checkpoints are cleared
	if len(o.Checkpoints()) != 0 {
		t.Errorf("expected 0 checkpoints after purge, got %d", len(o.Checkpoints()))
	}
}

func TestOverseer_Purge_OnEmptyOverseer(t *testing.T) {
	o := New("test goal", nil, types.Score(70), nil)

	// Verify no checkpoints initially
	if len(o.Checkpoints()) != 0 {
		t.Fatalf("expected 0 checkpoints initially, got %d", len(o.Checkpoints()))
	}

	// Purge on empty should not panic
	o.Purge()

	// Verify still empty
	if len(o.Checkpoints()) != 0 {
		t.Errorf("expected 0 checkpoints after purge on empty, got %d", len(o.Checkpoints()))
	}
}

func TestOverseer_Purge_AllowsNewCheckpointsAfter(t *testing.T) {
	o := New("test goal", nil, types.Score(70), nil)
	ctx := context.Background()

	// Add a checkpoint, then purge
	o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "before purge",
	})
	o.Purge()

	// Add new checkpoints after purge
	o.Evaluate(ctx, "agent-2", types.OverseerReport{
		NodeName:     "node-2",
		Completeness: 0.8,
		TestCoverage: 0.8,
		CodeQuality:  0.8,
		NoSideEffect: 0.8,
		Summary:      "after purge",
	})

	// Verify only the post-purge checkpoint exists
	cps := o.Checkpoints()
	if len(cps) != 1 {
		t.Fatalf("expected 1 checkpoint after purge and re-evaluate, got %d", len(cps))
	}
	if cps[0].ChildAgentID != "agent-2" {
		t.Errorf("expected agent-2, got %s", cps[0].ChildAgentID)
	}
}

func TestOverseer_Purge_IsIdempotent(t *testing.T) {
	o := New("test goal", nil, types.Score(70), nil)
	ctx := context.Background()

	o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "report",
	})

	// Purge multiple times should not panic
	o.Purge()
	o.Purge()
	o.Purge()

	if len(o.Checkpoints()) != 0 {
		t.Errorf("expected 0 checkpoints after multiple purges, got %d", len(o.Checkpoints()))
	}
}

func TestOverseer_Purge_PreservesGoalAndConstraints(t *testing.T) {
	goal := "preserved goal"
	constraints := map[string]any{"min_test_coverage": 0.9}
	threshold := types.Score(85)
	rules := DefaultScoringRules()

	o := New(goal, constraints, threshold, rules)
	ctx := context.Background()

	o.Evaluate(ctx, "agent-1", types.OverseerReport{
		NodeName:     "node-1",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  0.9,
		NoSideEffect: 0.9,
		Summary:      "report",
	})
	o.Purge()

	// Goal should be preserved
	if o.Goal() != goal {
		t.Errorf("expected goal %q, got %q", goal, o.Goal())
	}

	// Constraints should be preserved
	if o.Constraints()["min_test_coverage"] != 0.9 {
		t.Errorf("expected constraint min_test_coverage=0.9, got %v", o.Constraints()["min_test_coverage"])
	}
}
