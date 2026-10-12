package overseer

import (
	"context"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestOverseerWithCodeQualityChecker demonstrates the full integration flow:
// CodeQualityChecker analyzes code → OverseerReport gets populated → Overseer evaluates.
func TestCodeQualityIntegration_FullFlow(t *testing.T) {
	// Setup: create an Overseer with strict code_quality constraint
	constraints := map[string]any{"min_code_quality": 0.8}
	o := New("build auth module", constraints, types.Score(0.7), nil)
	cqc := NewCodeQualityChecker()

	ctx := context.Background()

	// Step 1: Analyze code quality
	cleanCode := `// Login authenticates a user with the given credentials.
// Returns a session token on success.
func Login(username, password string) (string, error) {
	if username == "" {
		return "", fmt.Errorf("username required")
	}
	return generateToken(username)
}`
	cqResult := cqc.Analyze(cleanCode)

	// Step 2: Build OverseerReport with code quality score from checker
	report := types.OverseerReport{
		NodeName:     "auth-node",
		Completeness: 0.9,
		TestCoverage: 0.85,
		CodeQuality:  cqResult.Score,
		NoSideEffect: 0.9,
		Summary:      "auth module implemented",
	}

	// Step 3: Evaluate
	passed := o.Evaluate(ctx, "agent-auth", report)

	// Clean code should have high quality score and pass
	if !passed {
		t.Errorf("expected clean code to pass evaluation, code quality score was %f", cqResult.Score)
	}
}

func TestCodeQualityIntegration_PoorCodeFailsConstraint(t *testing.T) {
	constraints := map[string]any{"min_code_quality": 0.8}
	o := New("build module", constraints, types.Score(0.7), nil)
	cqc := NewCodeQualityChecker()
	cqc.MaxLineLength = 40
	cqc.MaxFunctionLength = 5

	ctx := context.Background()

	// Poor quality code: long lines, long function
	poorCode := `func ProcessItems(items []string, config map[string]interface{}) ([]string, error) {
	var results []string
	for _, item := range items {
		processed, err := processSingleItem(item, config)
		if err != nil {
			return nil, fmt.Errorf("failed to process item %s: %w", item, err)
		}
		results = append(results, processed)
	}
	return results, nil
}`

	cqResult := cqc.Analyze(poorCode)

	report := types.OverseerReport{
		NodeName:     "process-node",
		Completeness: 0.9,
		TestCoverage: 0.9,
		CodeQuality:  cqResult.Score,
		NoSideEffect: 0.9,
		Summary:      "processor implemented",
	}

	passed := o.Evaluate(ctx, "agent-process", report)

	// Poor code quality should cause constraint failure
	if passed {
		t.Errorf("expected poor code to fail due to min_code_quality constraint, cq score was %f", cqResult.Score)
	}
}

func TestCodeQualityIntegration_CheckerResultFields(t *testing.T) {
	cqc := NewCodeQualityChecker()

	code := `// GoodFunc does something useful.
func GoodFunc(x int) int {
	return x * 2
}`

	result := cqc.Analyze(code)

	// Verify result structure
	if result.Score < 0.0 || result.Score > 1.0 {
		t.Errorf("score should be in [0.0, 1.0], got %f", result.Score)
	}
	if result.Issues == nil {
		t.Error("expected non-nil issues slice")
	}

	// Good code should score high
	if result.Score < 0.9 {
		t.Errorf("expected good code to score >= 0.9, got %f", result.Score)
	}
}

func TestCodeQualityIntegration_MultipleFunctions(t *testing.T) {
	cqc := NewCodeQualityChecker()
	cqc.MaxFunctionLength = 3

	code := `// Add adds two numbers.
func Add(a, b int) int {
	return a + b
}

// Multiply multiplies two numbers.
func Multiply(a, b int) int {
	return a * b
}

func LongFunc() {
	line1()
	line2()
	line3()
	line4()
	line5()
}`

	result := cqc.Analyze(code)

	// Should have 2 issues on LongFunc: missing comment + function too long
	if len(result.Issues) != 2 {
		t.Errorf("expected 2 issues (missing comment + long function), got %d: %v", len(result.Issues), result.Issues)
	}
}

func TestCodeQualityIntegration_OverseerEvaluateWithCodeQuality(t *testing.T) {
	// This test validates the pattern of using CodeQualityChecker
	// to populate the CodeQuality field before Overseer evaluation
	constraints := map[string]any{"min_code_quality": 0.7}
	o := New("test", constraints, types.Score(0.6), nil)
	cqc := NewCodeQualityChecker()
	ctx := context.Background()

	// Test various code quality scenarios
	tests := []struct {
		name         string
		code         string
		completeness float64
		testCoverage float64
		noSideEffect float64
		shouldPass   bool
	}{
		{
			name:         "clean code passes",
			code:         "// Foo does foo.\nfunc Foo() {\n\tbar()\n}",
			completeness: 0.9,
			testCoverage: 0.9,
			noSideEffect: 0.9,
			shouldPass:   true,
		},
		{
			name:         "uncommented code may still pass if other scores high",
			code:         "func Bar() {\n\tbaz()\n}",
			completeness: 0.95,
			testCoverage: 0.95,
			noSideEffect: 0.95,
			shouldPass:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cqResult := cqc.Analyze(tt.code)
			report := types.OverseerReport{
				NodeName:     "test-node",
				Completeness: tt.completeness,
				TestCoverage: tt.testCoverage,
				CodeQuality:  cqResult.Score,
				NoSideEffect: tt.noSideEffect,
				Summary:      tt.name,
			}
			passed := o.Evaluate(ctx, "test-agent", report)
			if passed != tt.shouldPass {
				t.Errorf("expected passed=%v, got %v (cqScore=%f, totalScore should reveal)", tt.shouldPass, passed, cqResult.Score)
			}
		})
	}
}
