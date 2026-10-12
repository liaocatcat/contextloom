package overseer

import "github.com/contextloom/contextloom-agent/pkg/types"

type ScoringRules struct {
	CompletenessWeight float64
	TestCoverageWeight float64
	CodeQualityWeight  float64
	NoSideEffectWeight float64
}

func DefaultScoringRules() *ScoringRules {
	return &ScoringRules{
		CompletenessWeight: 0.40,
		TestCoverageWeight: 0.30,
		CodeQualityWeight:  0.20,
		NoSideEffectWeight: 0.10,
	}
}

func (sr *ScoringRules) Calculate(report types.OverseerReport) types.Score {
	score := report.Completeness*sr.CompletenessWeight +
		report.TestCoverage*sr.TestCoverageWeight +
		report.CodeQuality*sr.CodeQualityWeight +
		report.NoSideEffect*sr.NoSideEffectWeight
	return types.Score(score)
}
