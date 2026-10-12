package harness

import (
	"reflect"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// --- tokenize ---

func TestTokenize_Empty(t *testing.T) {
	result := tokenize("")
	if len(result) != 0 {
		t.Errorf("expected empty slice, got %v", result)
	}
}

func TestTokenize_SimpleWords(t *testing.T) {
	result := tokenize("hello world")
	expected := []string{"hello", "world"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestTokenize_MixedCase(t *testing.T) {
	result := tokenize("Hello World")
	expected := []string{"hello", "world"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected lowercase %v, got %v", expected, result)
	}
}

func TestTokenize_WithPunctuation(t *testing.T) {
	result := tokenize("hello, world!")
	expected := []string{"hello", "world"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestTokenize_WithUnderscores(t *testing.T) {
	result := tokenize("test_case example_func")
	// 新契约：完整 token（精确命中）+ 下划线子词（组件式标识符可被部分命中）
	expected := []string{"test_case", "test", "case", "example_func", "example", "func"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestTokenize_WithDigits(t *testing.T) {
	result := tokenize("abc123 xyz")
	expected := []string{"abc123", "xyz"}
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("expected %v, got %v", expected, result)
	}
}

func TestTokenize_ChineseCharacters(t *testing.T) {
	// Chinese characters are not letters/digits/underscores, so they act as delimiters
	result := tokenize("hello世界world")
	if len(result) == 0 {
		t.Error("expected some tokens around Chinese characters")
	}
}

func TestTokenize_SpecialCharsOnly(t *testing.T) {
	result := tokenize("!@#$%^&*()")
	if len(result) != 0 {
		t.Errorf("expected empty for all special chars, got %v", result)
	}
}

// --- toSet ---

func TestToSet_Empty(t *testing.T) {
	result := toSet([]string{})
	if len(result) != 0 {
		t.Errorf("expected empty set, got %v", result)
	}
}

func TestToSet_UniqueItems(t *testing.T) {
	result := toSet([]string{"a", "b", "c"})
	if len(result) != 3 {
		t.Errorf("expected 3 items, got %d", len(result))
	}
	for _, k := range []string{"a", "b", "c"} {
		if !result[k] {
			t.Errorf("expected %q to be in set", k)
		}
	}
}

func TestToSet_Duplicates(t *testing.T) {
	result := toSet([]string{"a", "b", "a", "c", "b"})
	if len(result) != 3 {
		t.Errorf("expected 3 unique items, got %d", len(result))
	}
}

// --- RelevanceScorer ---

func TestNewRelevanceScorer_DefaultThreshold(t *testing.T) {
	s := NewRelevanceScorer()
	if s.threshold != 50 {
		t.Errorf("expected default threshold 50, got %v", s.threshold)
	}
}

func TestRelevanceScorer_Score_NilBlock(t *testing.T) {
	s := NewRelevanceScorer()
	score := s.Score("query", nil)
	if score != 0 {
		t.Errorf("expected 0 for nil block, got %v", score)
	}
}

func TestRelevanceScorer_Score_EmptyQuery(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{ID: "1", Content: "hello world"}
	score := s.Score("", block)
	if score != 0 {
		t.Errorf("expected 0 for empty query, got %v", score)
	}
}

func TestRelevanceScorer_Score_EmptyContent(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{ID: "1", Content: ""}
	score := s.Score("query", block)
	if score != 0 {
		t.Errorf("expected 0 for empty content, got %v", score)
	}
}

func TestRelevanceScorer_Score_PerfectMatch(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{ID: "1", Content: "hello world"}
	score := s.Score("hello world", block)
	if score <= 0 {
		t.Errorf("expected positive score for matching query, got %v", score)
	}
}

func TestRelevanceScorer_Score_NoMatch(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{ID: "1", Content: "foo bar baz"}
	score := s.Score("hello world", block)
	// With no matching tokens, overlapScore is 0, but recency may still apply
	if score > 15 {
		t.Errorf("expected max score of 15 (recency only) for no match, got %v", score)
	}
}

func TestRelevanceScorer_Score_RecentBlockGetsBonus(t *testing.T) {
	s := NewRelevanceScorer()

	oldBlock := &types.ContextBlock{
		ID:       "old",
		Content:  "hello world",
		LastUsed: time.Now().Add(-48 * time.Hour),
	}
	recentBlock := &types.ContextBlock{
		ID:       "recent",
		Content:  "hello world",
		LastUsed: time.Now(),
	}

	oldScore := s.Score("hello world", oldBlock)
	recentScore := s.Score("hello world", recentBlock)

	if recentScore <= oldScore {
		t.Errorf("recent block (%v) should score higher than old block (%v)", recentScore, oldScore)
	}
}

func TestRelevanceScorer_Score_ZeroLastUsed(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{
		ID:       "1",
		Content:  "hello world",
		LastUsed: time.Time{},
	}
	score := s.Score("hello world", block)
	// recency score is 15.0 (the "else" branch when IsZero is true)
	if score <= 0 {
		t.Errorf("expected positive score even with zero LastUsed, got %v", score)
	}
}

func TestRelevanceScorer_Score_RecentWithinHour(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{
		ID:       "1",
		Content:  "hello world",
		LastUsed: time.Now().Add(-30 * time.Minute),
	}
	score := s.Score("hello world", block)
	// Should get full recency bonus
	if score < 85 {
		t.Errorf("expected score >= 85 for recent block, got %v", score)
	}
}

func TestRelevanceScorer_Score_PartialMatch(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{ID: "1", Content: "hello world foo bar"}
	score := s.Score("hello world", block)
	// Partial match - should be less than perfect match
	fullBlock := &types.ContextBlock{ID: "2", Content: "hello world"}
	fullScore := s.Score("hello world", fullBlock)
	if score > fullScore {
		t.Errorf("partial match (%v) should not exceed full match (%v)", score, fullScore)
	}
}

func TestRelevanceScorer_Score_ClampedToMax100(t *testing.T) {
	s := NewRelevanceScorer()
	block := &types.ContextBlock{
		ID:       "1",
		Content:  "hello world",
		LastUsed: time.Now(),
	}
	score := s.Score("hello world", block)
	if score > 100 {
		t.Errorf("score should not exceed 100, got %v", score)
	}
}

func TestRelevanceScorer_SetThreshold(t *testing.T) {
	s := NewRelevanceScorer()
	s.SetThreshold(75)
	if s.threshold != 75 {
		t.Errorf("expected threshold 75, got %v", s.threshold)
	}
	if s.Threshold() != 75 {
		t.Errorf("Threshold() should return 75, got %v", s.Threshold())
	}
}
