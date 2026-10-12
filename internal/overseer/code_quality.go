package overseer

import (
	"math"
	"strings"
)

// CodeQualityChecker analyzes source code and produces a quality score (0.0 - 1.0)
// along with a list of detected issues.
type CodeQualityChecker struct {
	// MaxLineLength is the maximum allowed line length before penalty.
	MaxLineLength int
	// MaxFunctionLength is the maximum allowed function body lines before penalty.
	MaxFunctionLength int
	// RequireComments controls whether functions require a preceding comment.
	RequireComments bool
}

// CodeQualityIssue describes a single code quality problem found during analysis.
type CodeQualityIssue struct {
	Line        int     // 1-indexed line number where the issue occurs
	Severity    float64 // penalty weight for this issue (0.0 - 1.0)
	Description string  // human-readable description of the issue
}

// CodeQualityResult holds the result of a code quality analysis.
type CodeQualityResult struct {
	Score  float64
	Issues []CodeQualityIssue
}

// NewCodeQualityChecker creates a CodeQualityChecker with sensible defaults.
func NewCodeQualityChecker() *CodeQualityChecker {
	return &CodeQualityChecker{
		MaxLineLength:     120,
		MaxFunctionLength: 50,
		RequireComments:   true,
	}
}

// Analyze inspects the given source code and returns a quality score along with
// any issues found. The score ranges from 0.0 (worst) to 1.0 (best).
func (c *CodeQualityChecker) Analyze(code string) CodeQualityResult {
	if len(strings.TrimSpace(code)) == 0 {
		return CodeQualityResult{Score: 1.0, Issues: nil}
	}

	lines := strings.Split(code, "\n")
	issues := make([]CodeQualityIssue, 0)

	// Check line length
	for i, line := range lines {
		if len(line) > c.MaxLineLength {
			issues = append(issues, CodeQualityIssue{
				Line:        i + 1,
				Severity:    0.15,
				Description: "line too long",
			})
		}
	}

	// Check function length and missing comments
	inFunc := false
	funcStartLine := 0
	braceDepth := 0
	hasComment := false
	funcLineCount := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Track whether we've seen a comment before a function
		if !inFunc && strings.HasPrefix(trimmed, "//") {
			hasComment = true
			continue
		}

		if !inFunc && strings.HasPrefix(trimmed, "func ") {
			inFunc = true
			funcStartLine = i + 1
			funcLineCount = 0

			// Check for missing comment
			if c.RequireComments && !hasComment {
				issues = append(issues, CodeQualityIssue{
					Line:        funcStartLine,
					Severity:    0.10,
					Description: "missing comment before function",
				})
			}

			// Reset for next function
			hasComment = false
		}

		if inFunc {
			// Count braces to track function body
			for _, ch := range line {
				if ch == '{' {
					braceDepth++
				} else if ch == '}' {
					braceDepth--
					if braceDepth == 0 {
						// End of function
						if funcLineCount > c.MaxFunctionLength {
							issues = append(issues, CodeQualityIssue{
								Line:        funcStartLine,
								Severity:    0.20,
								Description: "function too long",
							})
						}
						inFunc = false
						funcLineCount = 0
					}
				}
			}
			funcLineCount++
		}
	}

	// Check for functions that end at EOF (no closing brace found yet)
	if inFunc && funcLineCount > c.MaxFunctionLength {
		issues = append(issues, CodeQualityIssue{
			Line:        funcStartLine,
			Severity:    0.20,
			Description: "function too long",
		})
	}

	// Calculate score: start at 1.0 and subtract penalties
	score := 1.0
	for _, issue := range issues {
		score -= issue.Severity
	}
	score = math.Max(0.0, score)

	return CodeQualityResult{
		Score:  score,
		Issues: issues,
	}
}
