package overseer

import (
	"math"
	"testing"
)

func TestNewCodeQualityChecker_DefaultConfig(t *testing.T) {
	cqc := NewCodeQualityChecker()

	if cqc.MaxLineLength != 120 {
		t.Errorf("expected MaxLineLength=120, got %d", cqc.MaxLineLength)
	}
	if cqc.MaxFunctionLength != 50 {
		t.Errorf("expected MaxFunctionLength=50, got %d", cqc.MaxFunctionLength)
	}
	if !cqc.RequireComments {
		t.Error("expected RequireComments=true by default")
	}
}

func TestCodeQualityChecker_PerfectCode(t *testing.T) {
	cqc := NewCodeQualityChecker()
	code := `// Add adds two integers and returns the result.
func Add(a, b int) int {
	return a + b
}`

	result := cqc.Analyze(code)
	if result.Score < 0.95 {
		t.Errorf("expected near-perfect score for clean code, got %f", result.Score)
	}
	if len(result.Issues) != 0 {
		t.Errorf("expected 0 issues, got %d: %v", len(result.Issues), result.Issues)
	}
}

func TestCodeQualityChecker_LongLines(t *testing.T) {
	cqc := NewCodeQualityChecker()
	cqc.MaxLineLength = 80

	// This line is over 80 chars
	code := `func Foo() {
	x := "this is a very long string that exceeds the maximum line length limit of eighty characters"
}`

	result := cqc.Analyze(code)
	if result.Score >= 0.9 {
		t.Errorf("expected score below 0.9 due to long line, got %f", result.Score)
	}

	hasLongLineIssue := false
	for _, issue := range result.Issues {
		if issue.Description == "line too long" {
			hasLongLineIssue = true
			break
		}
	}
	if !hasLongLineIssue {
		t.Error("expected 'line too long' issue")
	}
}

func TestCodeQualityChecker_LongFunction(t *testing.T) {
	cqc := NewCodeQualityChecker()
	cqc.MaxFunctionLength = 5

	// Function with more than 5 lines
	code := `func ManyLines() {
	a := 1
	b := 2
	c := 3
	d := 4
	e := 5
	f := 6
}`

	result := cqc.Analyze(code)
	if result.Score >= 0.9 {
		t.Errorf("expected score below 0.9 due to long function, got %f", result.Score)
	}

	hasLongFuncIssue := false
	for _, issue := range result.Issues {
		if issue.Description == "function too long" {
			hasLongFuncIssue = true
			break
		}
	}
	if !hasLongFuncIssue {
		t.Error("expected 'function too long' issue")
	}
}

func TestCodeQualityChecker_MissingComments(t *testing.T) {
	cqc := NewCodeQualityChecker()
	cqc.RequireComments = true

	code := `func NoComment(a int) int {
	return a * 2
}`

	result := cqc.Analyze(code)
	if result.Score >= 0.95 {
		t.Errorf("expected score below 0.95 due to missing comment, got %f", result.Score)
	}

	hasMissingCommentIssue := false
	for _, issue := range result.Issues {
		if issue.Description == "missing comment before function" {
			hasMissingCommentIssue = true
			break
		}
	}
	if !hasMissingCommentIssue {
		t.Error("expected 'missing comment before function' issue")
	}
}

func TestCodeQualityChecker_EmptyCode(t *testing.T) {
	cqc := NewCodeQualityChecker()

	result := cqc.Analyze("")
	if math.Abs(result.Score-1.0) > 0.0001 {
		t.Errorf("expected perfect score for empty code, got %f", result.Score)
	}
	if len(result.Issues) != 0 {
		t.Errorf("expected 0 issues for empty code, got %d", len(result.Issues))
	}
}

func TestCodeQualityChecker_MultipleIssues(t *testing.T) {
	cqc := NewCodeQualityChecker()
	cqc.MaxLineLength = 30
	cqc.MaxFunctionLength = 3

	code := `func ThisIsAVeryLongFunctionNameThatExceedsLimit() {
	doSomething()
	doAnotherThing()
	yetAnotherThing()
	oneMoreThing()
}`

	result := cqc.Analyze(code)
	// Should have both long line and long function issues
	if len(result.Issues) < 2 {
		t.Errorf("expected at least 2 issues, got %d: %v", len(result.Issues), result.Issues)
	}
	if result.Score >= 0.8 {
		t.Errorf("expected score below 0.8 with multiple issues, got %f", result.Score)
	}
}

func TestCodeQualityChecker_ScoreRange(t *testing.T) {
	cqc := NewCodeQualityChecker()

	// Test various code states
	tests := []struct {
		name string
		code string
		min  float64
		max  float64
	}{
		{"empty", "", 0.95, 1.0},
		{"simple func", "func Add(a, b int) int { return a + b }", 0.7, 1.0},
		{"commented func", "// Add adds two ints.\nfunc Add(a, b int) int {\n\treturn a + b\n}", 0.85, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cqc.Analyze(tt.code)
			if result.Score < tt.min || result.Score > tt.max {
				t.Errorf("expected score between %f and %f, got %f", tt.min, tt.max, result.Score)
			}
		})
	}
}

func TestCodeQualityChecker_CustomConfig(t *testing.T) {
	cqc := &CodeQualityChecker{
		MaxLineLength:     200,
		MaxFunctionLength: 200,
		RequireComments:   false,
	}

	code := `func ManyLines() {
	a := 1
	b := 2
	c := 3
	d := 4
	e := 5
	f := 6
}`

	result := cqc.Analyze(code)
	if result.Score < 0.9 {
		t.Errorf("expected high score with lenient config, got %f. Issues: %v", result.Score, result.Issues)
	}
}

func TestCodeQualityChecker_ScoreNeverNegative(t *testing.T) {
	cqc := NewCodeQualityChecker()
	cqc.MaxLineLength = 10
	cqc.MaxFunctionLength = 2

	code := `func AVeryVeryLongFunctionNameThatGoesOnAndOn() {
	line1
	line2
	line3
	line4
	line5
}`

	result := cqc.Analyze(code)
	if result.Score < 0.0 {
		t.Errorf("score should never be negative, got %f", result.Score)
	}
}
