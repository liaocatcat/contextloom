package overseer

import (
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestVerifyExecution_BothNil(t *testing.T) {
	sim, trusted := VerifyExecution(nil, nil)
	if sim != 0 || trusted {
		t.Errorf("expected (0, false), got (%f, %v)", sim, trusted)
	}
}

func TestVerifyExecution_ClaimedNil(t *testing.T) {
	actual := &types.ToolResult{Result: map[string]any{"passed": 5.0, "failed": 0.0}}
	sim, trusted := VerifyExecution(nil, actual)
	if sim != 0 || trusted {
		t.Errorf("expected (0, false), got (%f, %v)", sim, trusted)
	}
}

func TestVerifyExecution_PerfectMatch(t *testing.T) {
	claimed := &types.ToolResult{Result: map[string]any{"passed": 5.0, "failed": 0.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 5.0, "failed": 0.0}}
	sim, trusted := VerifyExecution(claimed, actual)
	if sim != 1.0 || !trusted {
		t.Errorf("expected (1.0, true), got (%f, %v)", sim, trusted)
	}
}

func TestVerifyExecution_BoundaryThreshold(t *testing.T) {
	claimed := &types.ToolResult{Result: map[string]any{"passed": 4.0, "failed": 1.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 5.0, "failed": 0.0}}
	// matched: min(4,5)=4 + min(1,0)=0 = 4, total: max(4,5)+max(1,0)=5+1=6
	// similarity = 4/6 ≈ 0.667
	sim, trusted := VerifyExecution(claimed, actual)
	if trusted {
		t.Errorf("expected untrusted at similarity=%f", sim)
	}
}

func TestVerifyExecution_CheatingDetected(t *testing.T) {
	claimed := &types.ToolResult{Result: map[string]any{"passed": 10.0, "failed": 0.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 2.0, "failed": 8.0}}
	sim, trusted := VerifyExecution(claimed, actual)
	if trusted {
		t.Errorf("CHEATING should be detected, got similarity=%f trusted=%v", sim, trusted)
	}
}

func TestVerifyExecution_HighButTrusted(t *testing.T) {
	claimed := &types.ToolResult{Result: map[string]any{"passed": 9.0, "failed": 1.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 8.0, "failed": 2.0}}
	// matched: min(9,8)=8 + min(1,2)=1 = 9, total: max(9,8)+max(1,2)=9+2=11
	// similarity = 9/11 ≈ 0.818
	sim, trusted := VerifyExecution(claimed, actual)
	if sim < 0.8 || !trusted {
		t.Errorf("expected trusted (≥0.8), got similarity=%f trusted=%v", sim, trusted)
	}
}

func TestVerifyExecution_ZeroTotal(t *testing.T) {
	claimed := &types.ToolResult{Result: map[string]any{"passed": 0.0, "failed": 0.0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 0.0, "failed": 0.0}}
	sim, trusted := VerifyExecution(claimed, actual)
	if sim != 0 || trusted {
		t.Errorf("expected (0, false) for empty results, got (%f, %v)", sim, trusted)
	}
}

func TestVerifyExecution_IntPassed(t *testing.T) {
	claimed := &types.ToolResult{Result: map[string]any{"passed": 3, "failed": 0}}
	actual := &types.ToolResult{Result: map[string]any{"passed": 3, "failed": 0}}
	sim, trusted := VerifyExecution(claimed, actual)
	if sim != 1.0 || !trusted {
		t.Errorf("expected (1.0, true) for int types, got (%f, %v)", sim, trusted)
	}
}
