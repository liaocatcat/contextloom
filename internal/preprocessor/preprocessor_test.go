package preprocessor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// =============================================================================
// Preprocessor.New tests
// =============================================================================

func TestPreprocessor_New(t *testing.T) {
	p := New("/some/path/vocab.json")
	if p == nil {
		t.Fatal("New() returned nil")
	}
	if p.mapper == nil {
		t.Error("Preprocessor.mapper is nil")
	}
	if p.vocab == nil {
		t.Error("Preprocessor.vocab is nil")
	}
}

func TestPreprocessor_New_EmptyPath(t *testing.T) {
	p := New("")
	if p == nil {
		t.Fatal("New() with empty path should not return nil")
	}
	// Should still be usable
	result, err := p.Process("fix the bug")
	if err != nil {
		t.Fatalf("Process with empty path should not error: %v", err)
	}
	if result == nil {
		t.Fatal("Process returned nil result")
	}
}

// =============================================================================
// Preprocessor.Process tests
// =============================================================================

func TestPreprocessor_Process_Simple(t *testing.T) {
	p := New("")
	result, err := p.Process("fix the null pointer bug")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if result == nil {
		t.Fatal("Process() returned nil")
	}

	// Should detect BUG_FIX intent
	if result.Task != "BUG_FIX" {
		t.Errorf("expected Task 'BUG_FIX', got %q", result.Task)
	}

	// Should have CODE_AGENT directive
	if result.Directive != string(types.DirectiveCodeAgent) {
		t.Errorf("expected Directive 'CODE_AGENT', got %q", result.Directive)
	}
}

func TestPreprocessor_Process_FeatureRequest(t *testing.T) {
	p := New("")
	result, err := p.Process("add a new function to the module")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	if result.Task != "FEATURE" {
		t.Errorf("expected Task 'FEATURE', got %q", result.Task)
	}
}

func TestPreprocessor_Process_ExplainRequest(t *testing.T) {
	p := New("")
	result, err := p.Process("explain how this function works")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	if result.Task != "EXPLAIN" {
		t.Errorf("expected Task 'EXPLAIN', got %q", result.Task)
	}
}

func TestPreprocessor_Process_ScopeContainsVocabMappings(t *testing.T) {
	p := New("")

	// "null_pointer" is a default vocab entry -> "NPE"
	result, err := p.Process("fix null_pointer in function")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	// Check that Scope contains the vocab-mapped value "NPE"
	foundNPE := false
	for _, s := range result.Scope {
		if s == "NPE" {
			foundNPE = true
			break
		}
	}
	if !foundNPE {
		t.Errorf("expected Scope to contain 'NPE', got %v", result.Scope)
	}
}

func TestPreprocessor_Process_EmptyInput(t *testing.T) {
	p := New("")
	result, err := p.Process("")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	if result == nil {
		t.Fatal("Process() returned nil for empty input")
	}
	if result.Task != "TASK" {
		t.Errorf("expected default Task 'TASK' for empty input, got %q", result.Task)
	}
}

func TestPreprocessor_Process_NoVocabFile(t *testing.T) {
	// Use a path that doesn't exist - should still work using defaults
	p := New("/nonexistent/path/vocab.json")
	result, err := p.Process("refactor the module")
	if err != nil {
		t.Fatalf("Process() should not error: %v", err)
	}
	if result == nil {
		t.Fatal("Process() returned nil")
	}
	if result.Task != "REFACTOR" {
		t.Errorf("expected Task 'REFACTOR', got %q", result.Task)
	}
}

// =============================================================================
// StructuredPrompt tests
// =============================================================================

func TestStructuredPrompt_JSON(t *testing.T) {
	sp := &StructuredPrompt{
		Directive: string(types.DirectiveCodeAgent),
		Task:      "BUG_FIX",
		Scope:     []string{"NPE", "FUNC"},
		Constraints: map[string]any{
			"max_lines": 100,
		},
		ContextKeys: []string{"session-1"},
	}

	data, err := sp.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	// Verify it's valid JSON by unmarshaling
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v", err)
	}

	if parsed["directive"] != "CODE_AGENT" {
		t.Errorf("expected directive 'CODE_AGENT', got %v", parsed["directive"])
	}
	if parsed["task"] != "BUG_FIX" {
		t.Errorf("expected task 'BUG_FIX', got %v", parsed["task"])
	}

	scope, ok := parsed["scope"].([]any)
	if !ok {
		t.Fatal("scope should be an array")
	}
	if len(scope) != 2 {
		t.Errorf("expected 2 scope items, got %d", len(scope))
	}
}

func TestStructuredPrompt_JSON_OmitsEmptyFields(t *testing.T) {
	sp := &StructuredPrompt{
		Directive: string(types.DirectiveCodeAgent),
		Task:      "EXPLAIN",
		// Scope, Constraints, ContextKeys are empty
	}

	data, err := sp.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v", err)
	}

	// scope should be omitted when empty (omitempty tag)
	if _, exists := parsed["scope"]; exists {
		t.Error("scope should be omitted when empty")
	}
	if _, exists := parsed["constraints"]; exists {
		t.Error("constraints should be omitted when empty")
	}
	if _, exists := parsed["context_keys"]; exists {
		t.Error("context_keys should be omitted when empty")
	}
}

func TestStructuredPrompt_JSON_EmptyScope(t *testing.T) {
	sp := &StructuredPrompt{
		Directive: string(types.DirectiveCodeAgent),
		Task:      "TASK",
		Scope:     []string{},
	}

	data, err := sp.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v", err)
	}

	// Empty slice with omitempty should be omitted
	if _, exists := parsed["scope"]; exists {
		t.Error("empty scope should be omitted in JSON output")
	}
}

func TestStructuredPrompt_JSON_WithConstraints(t *testing.T) {
	sp := &StructuredPrompt{
		Directive: string(types.DirectiveCodeAgent),
		Task:      "FEATURE",
		Constraints: map[string]any{
			"language":   "go",
			"max_files":  float64(5),
			"with_tests": true,
		},
	}

	data, err := sp.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("JSON() produced invalid JSON: %v", err)
	}

	constraints, ok := parsed["constraints"].(map[string]any)
	if !ok {
		t.Fatal("constraints should be an object")
	}
	if constraints["language"] != "go" {
		t.Errorf("expected language 'go', got %v", constraints["language"])
	}
	if constraints["with_tests"] != true {
		t.Errorf("expected with_tests true, got %v", constraints["with_tests"])
	}
}

// =============================================================================
// Integration tests: Preprocessor.Process + StructuredPrompt.JSON
// =============================================================================

func TestPreprocessor_Process_JSONRoundTrip(t *testing.T) {
	p := New("")
	result, err := p.Process("fix bug in the authentication module")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	data, err := result.JSON()
	if err != nil {
		t.Fatalf("JSON() error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Verify core fields
	if parsed["directive"] != "CODE_AGENT" {
		t.Errorf("expected directive 'CODE_AGENT', got %v", parsed["directive"])
	}
	if parsed["task"] != "BUG_FIX" {
		t.Errorf("expected task 'BUG_FIX', got %v", parsed["task"])
	}
}

// =============================================================================
// Preprocessor with real vocab file tests
// =============================================================================

func TestPreprocessor_WithTempVocabFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-preprocessor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})

	// Create a vocab.json file with custom mappings
	vocabPath := filepath.Join(tmpDir, "vocab.json")
	vocabContent := `{"my_custom_term": "MCT", "another_term": "AT"}`
	if err := os.WriteFile(vocabPath, []byte(vocabContent), 0644); err != nil {
		t.Fatalf("failed to write vocab file: %v", err)
	}

	p := New(vocabPath)
	result, err := p.Process("explain my_custom_term in the function")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	if result.Task != "EXPLAIN" {
		t.Errorf("expected Task 'EXPLAIN', got %q", result.Task)
	}

	// Note: vocab file won't be loaded by default, Load() must be called explicitly.
	// This test verifies the preprocessor still works gracefully.
	if result.Directive != string(types.DirectiveCodeAgent) {
		t.Errorf("expected Directive 'CODE_AGENT', got %q", result.Directive)
	}
}

func TestPreprocessor_LoadThenProcess(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-preprocessor-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() {
		os.RemoveAll(tmpDir)
	})

	vocabPath := filepath.Join(tmpDir, "vocab.json")
	vocabContent := `{"my_custom_term": "MCT"}`
	if err := os.WriteFile(vocabPath, []byte(vocabContent), 0644); err != nil {
		t.Fatalf("failed to write vocab file: %v", err)
	}

	p := New(vocabPath)

	// Explicitly load the vocab
	if err := p.vocab.Load(); err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	result, err := p.Process("explain my_custom_term")
	if err != nil {
		t.Fatalf("Process() error: %v", err)
	}

	if result.Task != "EXPLAIN" {
		t.Errorf("expected Task 'EXPLAIN', got %q", result.Task)
	}
}
