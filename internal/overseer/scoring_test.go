package overseer

import (
	"math"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestDefaultScoringRules_Weights(t *testing.T) {
	rules := DefaultScoringRules()

	if rules.CompletenessWeight != 0.40 {
		t.Errorf("expected CompletenessWeight=0.40, got %f", rules.CompletenessWeight)
	}
	if rules.TestCoverageWeight != 0.30 {
		t.Errorf("expected TestCoverageWeight=0.30, got %f", rules.TestCoverageWeight)
	}
	if rules.CodeQualityWeight != 0.20 {
		t.Errorf("expected CodeQualityWeight=0.20, got %f", rules.CodeQualityWeight)
	}
	if rules.NoSideEffectWeight != 0.10 {
		t.Errorf("expected NoSideEffectWeight=0.10, got %f", rules.NoSideEffectWeight)
	}
}

func TestDefaultScoringRules_WeightsSumToOne(t *testing.T) {
	rules := DefaultScoringRules()
	sum := rules.CompletenessWeight +
		rules.TestCoverageWeight +
		rules.CodeQualityWeight +
		rules.NoSideEffectWeight

	if math.Abs(sum-1.0) > 0.0001 {
		t.Errorf("expected weights to sum to 1.0, got %f", sum)
	}
}

func TestCalculate_PerfectScore(t *testing.T) {
	rules := DefaultScoringRules()
	report := types.OverseerReport{
		Completeness: 1.0,
		TestCoverage: 1.0,
		CodeQuality:  1.0,
		NoSideEffect: 1.0,
	}

	score := rules.Calculate(report)
	if math.Abs(float64(score)-1.0) > 0.0001 {
		t.Errorf("expected perfect score of 1.0, got %f", score)
	}
}

func TestCalculate_ZeroScore(t *testing.T) {
	rules := DefaultScoringRules()
	report := types.OverseerReport{
		Completeness: 0.0,
		TestCoverage: 0.0,
		CodeQuality:  0.0,
		NoSideEffect: 0.0,
	}

	score := rules.Calculate(report)
	if score != 0.0 {
		t.Errorf("expected zero score of 0.0, got %f", score)
	}
}

func TestCalculate_WeightedAverage(t *testing.T) {
	rules := DefaultScoringRules()
	// Only code quality metric is perfect, everything else is zero
	report := types.OverseerReport{
		Completeness: 0.0,
		TestCoverage: 0.0,
		CodeQuality:  1.0,
		NoSideEffect: 0.0,
	}

	score := rules.Calculate(report)
	expected := types.Score(0.20) // only CodeQualityWeight (0.20) contributes
	if score != expected {
		t.Errorf("expected score %f, got %f", expected, score)
	}
}

func TestCalculate_CustomWeights(t *testing.T) {
	rules := &ScoringRules{
		CompletenessWeight: 0.25,
		TestCoverageWeight: 0.25,
		CodeQualityWeight:  0.25,
		NoSideEffectWeight: 0.25,
	}
	report := types.OverseerReport{
		Completeness: 0.8,
		TestCoverage: 0.6,
		CodeQuality:  0.9,
		NoSideEffect: 0.7,
	}
	score := rules.Calculate(report)
	expected := types.Score(0.8*0.25 + 0.6*0.25 + 0.9*0.25 + 0.7*0.25)
	if score != expected {
		t.Errorf("expected score %f, got %f", expected, score)
	}
}
