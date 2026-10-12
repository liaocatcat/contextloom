package overseer

import "github.com/contextloom/contextloom-agent/pkg/types"

func VerifyExecution(claimed, actual *types.ToolResult) (similarity float64, trusted bool) {
	if claimed == nil || actual == nil {
		return 0, false
	}

	claimedPassed, claimedFailed := extractTestCounts(claimed)
	actualPassed, actualFailed := extractTestCounts(actual)

	matched := 0
	total := 0

	// Compare passed counts
	minPassed := claimedPassed
	if actualPassed < minPassed {
		minPassed = actualPassed
	}
	matched += minPassed
	total += maxInt(claimedPassed, actualPassed)

	// Compare failed counts
	minFailed := claimedFailed
	if actualFailed < minFailed {
		minFailed = actualFailed
	}
	matched += minFailed
	total += maxInt(claimedFailed, actualFailed)

	if total == 0 {
		return 0, false
	}

	similarity = float64(matched) / float64(total)
	trusted = similarity >= 0.8
	return similarity, trusted
}

func extractTestCounts(result *types.ToolResult) (passed, failed int) {
	if result == nil {
		return 0, 0
	}

	// Result is stored as map[string]any with "passed" and "failed" fields
	resultMap, ok := result.Result.(map[string]any)
	if !ok {
		// Try numeric: "passed" might be bool or int or float64
		if countMap, ok := result.Result.(map[string]interface{}); ok {
			return extractFromInterfaceMap(countMap)
		}
		return 0, 0
	}

	passed = toInt(resultMap["passed"])
	failed = toInt(resultMap["failed"])
	return passed, failed
}

func extractFromInterfaceMap(m map[string]interface{}) (passed, failed int) {
	passed = toInt(m["passed"])
	failed = toInt(m["failed"])
	return passed, failed
}

func toInt(v any) int {
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	case bool:
		if val {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
