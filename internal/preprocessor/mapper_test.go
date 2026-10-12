package preprocessor

import (
	"testing"
)

// =============================================================================
// splitWords tests
// =============================================================================

func TestSplitWords_SimpleInput(t *testing.T) {
	result := splitWords("hello world")
	expected := []string{"hello", "world"}
	assertEqualSlices(t, result, expected)
}

func TestSplitWords_Punctuation(t *testing.T) {
	result := splitWords("fix the bug!!!")
	expected := []string{"fix", "the", "bug"}
	assertEqualSlices(t, result, expected)
}

func TestSplitWords_MixedSymbols(t *testing.T) {
	result := splitWords("add-this_method.test/file")
	expected := []string{"add-this_method.test/file"}
	assertEqualSlices(t, result, expected)
}

func TestSplitWords_Numbers(t *testing.T) {
	result := splitWords("test123 foo_bar")
	expected := []string{"test123", "foo_bar"}
	assertEqualSlices(t, result, expected)
}

func TestSplitWords_EmptyInput(t *testing.T) {
	result := splitWords("")
	if len(result) != 0 {
		t.Fatalf("expected empty slice, got %v", result)
	}
}

func TestSplitWords_OnlySymbols(t *testing.T) {
	result := splitWords("!@#$%^&*()")
	if len(result) != 0 {
		t.Fatalf("expected empty slice, got %v", result)
	}
}

func TestSplitWords_ChineseCharacters(t *testing.T) {
	// Chinese characters are letters, so they should be kept
	result := splitWords("修复bug")
	// Each Chinese character is a letter, and "bug" is kept
	if len(result) == 0 {
		t.Fatal("expected non-empty result for CJK input")
	}
}

// =============================================================================
// SemanticMapper.Parse tests - Intent Detection
// =============================================================================

func TestSemanticMapper_Parse_IntentFix(t *testing.T) {
	mapper := NewSemanticMapper()

	tests := []struct {
		input          string
		expectedIntent string
	}{
		{"fix the null pointer", "BUG_FIX"},
		{"there is a bug in the function", "BUG_FIX"},
		{"error handling in module", "BUG_FIX"},
		{"crash on startup", "BUG_FIX"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			intent, _ := mapper.Parse(tt.input)
			if intent != tt.expectedIntent {
				t.Errorf("expected intent %s, got %s", tt.expectedIntent, intent)
			}
		})
	}
}

func TestSemanticMapper_Parse_IntentRefactor(t *testing.T) {
	mapper := NewSemanticMapper()

	tests := []struct {
		input          string
		expectedIntent string
	}{
		{"refactor the module", "REFACTOR"},
		{"restructure the class", "REFACTOR"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			intent, _ := mapper.Parse(tt.input)
			if intent != tt.expectedIntent {
				t.Errorf("expected intent %s, got %s", tt.expectedIntent, intent)
			}
		})
	}
}

func TestSemanticMapper_Parse_IntentFeature(t *testing.T) {
	mapper := NewSemanticMapper()

	tests := []struct {
		input          string
		expectedIntent string
	}{
		{"add new function", "FEATURE"},
		{"new api endpoint", "FEATURE"},
		{"implement the interface", "FEATURE"},
		{"create a class", "FEATURE"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			intent, _ := mapper.Parse(tt.input)
			if intent != tt.expectedIntent {
				t.Errorf("expected intent %s, got %s", tt.expectedIntent, intent)
			}
		})
	}
}

func TestSemanticMapper_Parse_IntentExplain(t *testing.T) {
	mapper := NewSemanticMapper()

	tests := []struct {
		input          string
		expectedIntent string
	}{
		{"explain this function", "EXPLAIN"},
		{"describe the module", "EXPLAIN"},
		{"what does this do", "EXPLAIN"},
		{"how does it work", "EXPLAIN"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			intent, _ := mapper.Parse(tt.input)
			if intent != tt.expectedIntent {
				t.Errorf("expected intent %s, got %s", tt.expectedIntent, intent)
			}
		})
	}
}

func TestSemanticMapper_Parse_IntentTest(t *testing.T) {
	mapper := NewSemanticMapper()

	tests := []struct {
		input          string
		expectedIntent string
	}{
		{"write a test for this", "TEST"},
		{"add spec for module", "FEATURE"},
		{"verify the function", "TEST"},
		{"check the error", "TEST"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			intent, _ := mapper.Parse(tt.input)
			if intent != tt.expectedIntent {
				t.Errorf("expected intent %s, got %s", tt.expectedIntent, intent)
			}
		})
	}
}

func TestSemanticMapper_Parse_IntentOptimize(t *testing.T) {
	mapper := NewSemanticMapper()

	tests := []struct {
		input          string
		expectedIntent string
	}{
		{"optimize this function", "OPTIMIZE"},
		{"this is too slow", "OPTIMIZE"},
		{"make it faster", "OPTIMIZE"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			intent, _ := mapper.Parse(tt.input)
			if intent != tt.expectedIntent {
				t.Errorf("expected intent %s, got %s", tt.expectedIntent, intent)
			}
		})
	}
}

func TestSemanticMapper_Parse_IntentDefault(t *testing.T) {
	mapper := NewSemanticMapper()

	// Input with no intent keywords should default to "TASK"
	intent, _ := mapper.Parse("hello world")
	if intent != "TASK" {
		t.Errorf("expected default intent TASK, got %s", intent)
	}
}

func TestSemanticMapper_Parse_IntentMostFrequent(t *testing.T) {
	mapper := NewSemanticMapper()

	// Multiple intent words: "fix" and "add" both appear once,
	// but "bug" and "feature" cancel out. Most frequent wins.
	intent, _ := mapper.Parse("fix bug and add new feature")
	// "fix" -> BUG_FIX, "bug" -> BUG_FIX, "add" -> FEATURE, "new" -> FEATURE
	// BUG_FIX: 2, FEATURE: 2 -> tie, first encountered "fix" wins
	if intent != "BUG_FIX" && intent != "FEATURE" {
		t.Errorf("expected BUG_FIX or FEATURE, got %s", intent)
	}
}

// =============================================================================
// SemanticMapper.Parse tests - Entity Extraction
// =============================================================================

func TestSemanticMapper_Parse_EntityExtraction(t *testing.T) {
	mapper := NewSemanticMapper()

	_, entities := mapper.Parse("fix the function and class")
	// "fix" is an intent word, skipped; "function" -> FUNC, "class" -> CLASS
	// "the" (3 chars) and "and" (3 chars) are >2 chars and not in any map, included as-is
	expected := []string{"the", "FUNC", "and", "CLASS"}
	assertEqualSlices(t, entities, expected)
}

func TestSemanticMapper_Parse_EntityDeduplication(t *testing.T) {
	mapper := NewSemanticMapper()

	_, entities := mapper.Parse("function function function")
	expected := []string{"FUNC"}
	assertEqualSlices(t, entities, expected)
}

func TestSemanticMapper_Parse_EntityUnknownKept(t *testing.T) {
	mapper := NewSemanticMapper()

	_, entities := mapper.Parse("fix the authentication module")
	// "fix" is intent; "authentication" is >2 chars, not in entities map -> kept as-is
	// "module" -> MOD
	foundAuth := false
	foundMod := false
	for _, e := range entities {
		if e == "authentication" {
			foundAuth = true
		}
		if e == "MOD" {
			foundMod = true
		}
	}
	if !foundAuth {
		t.Errorf("expected 'authentication' in entities, got %v", entities)
	}
	if !foundMod {
		t.Errorf("expected 'MOD' in entities, got %v", entities)
	}
}

func TestSemanticMapper_Parse_EntityShortWordsFiltered(t *testing.T) {
	mapper := NewSemanticMapper()

	// Words <=2 chars that aren't in the entities map should be filtered
	_, entities := mapper.Parse("fix a b c")
	// "fix" is intent; "a", "b", "c" are all <=2 chars and not in entities map
	// So they should be filtered out
	for _, e := range entities {
		if e == "a" || e == "b" || e == "c" {
			t.Errorf("short unknown words should be filtered, got %v", entities)
		}
	}
}

func TestSemanticMapper_Parse_EmptyInput(t *testing.T) {
	mapper := NewSemanticMapper()

	intent, entities := mapper.Parse("")
	if intent != "TASK" {
		t.Errorf("expected default intent TASK for empty input, got %s", intent)
	}
	if len(entities) != 0 {
		t.Errorf("expected no entities for empty input, got %v", entities)
	}
}

// =============================================================================
// SemanticMapper.Parse tests - Edge Cases
// =============================================================================

func TestSemanticMapper_Parse_CaseInsensitivity(t *testing.T) {
	mapper := NewSemanticMapper()

	intent, _ := mapper.Parse("FIX the BUG")
	if intent != "BUG_FIX" {
		t.Errorf("expected BUG_FIX, got %s", intent)
	}
}

func TestSemanticMapper_Parse_IntentWithEntityOverlap(t *testing.T) {
	mapper := NewSemanticMapper()

	// "test" is both an intent (TEST) and an entity (TEST).
	// As an intent word, it should be skipped during entity extraction.
	intent, entities := mapper.Parse("test the test module")
	if intent != "TEST" {
		t.Errorf("expected TEST intent, got %s", intent)
	}
	// "module" -> MOD, but "test" should be skipped as it's an intent word
	for _, e := range entities {
		if e == "TEST" {
			t.Errorf("entities should not contain intent-word 'TEST', got %v", entities)
		}
	}
}

func TestSemanticMapper_Parse_OnlyIntentWords(t *testing.T) {
	mapper := NewSemanticMapper()

	intent, entities := mapper.Parse("fix bug refactor")
	// Multiple intents: BUG_FIX and REFACTOR, BUG_FIX appears first with 2 matches
	if intent == "TASK" {
		t.Errorf("expected non-default intent, got TASK")
	}
	if len(entities) != 0 {
		t.Errorf("expected no entities when only intent words present, got %v", entities)
	}
}

// =============================================================================
// Helpers
// =============================================================================

func assertEqualSlices(t *testing.T, got, expected []string) {
	t.Helper()
	if len(got) != len(expected) {
		t.Fatalf("length mismatch: got %v (%d), expected %v (%d)", got, len(got), expected, len(expected))
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Errorf("at index %d: got %q, expected %q", i, got[i], expected[i])
		}
	}
}
