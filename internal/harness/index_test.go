package harness

import (
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// --- HybridIndex ---

func TestNewHybridIndex(t *testing.T) {
	idx := NewHybridIndex()
	if idx == nil {
		t.Fatal("NewHybridIndex returned nil")
	}
	if idx.inverted == nil {
		t.Error("inverted index map should not be nil")
	}
	if idx.blocks == nil {
		t.Error("blocks map should not be nil")
	}
	if len(idx.inverted) != 0 {
		t.Errorf("inverted index should be empty, got %d entries", len(idx.inverted))
	}
	if len(idx.blocks) != 0 {
		t.Errorf("blocks should be empty, got %d entries", len(idx.blocks))
	}
}

func TestHybridIndex_Add_Single(t *testing.T) {
	idx := NewHybridIndex()
	block := &types.ContextBlock{
		ID:      "block-1",
		Content: "hello world",
		Score:   80,
	}
	idx.Add(block)

	// Verify block was stored
	retrieved, ok := idx.Get("block-1")
	if !ok {
		t.Fatal("block not found after Add")
	}
	if retrieved.ID != "block-1" {
		t.Errorf("expected ID 'block-1', got %q", retrieved.ID)
	}
	if retrieved.Content != "hello world" {
		t.Errorf("expected content 'hello world', got %q", retrieved.Content)
	}

	// Verify inverted index
	if _, exists := idx.inverted["hello"]; !exists {
		t.Error("token 'hello' should be in inverted index")
	}
	if _, exists := idx.inverted["world"]; !exists {
		t.Error("token 'world' should be in inverted index")
	}
}

func TestHybridIndex_Add_Multiple(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "foo bar"})
	idx.Add(&types.ContextBlock{ID: "2", Content: "bar baz"})

	if len(idx.blocks) != 2 {
		t.Errorf("expected 2 blocks, got %d", len(idx.blocks))
	}

	// 'bar' token should point to both blocks
	barIDs := idx.inverted["bar"]
	if len(barIDs) != 2 {
		t.Errorf("token 'bar' should have 2 block IDs, got %d", len(barIDs))
	}
}

func TestHybridIndex_Add_Overwrite(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "old content"})
	idx.Add(&types.ContextBlock{ID: "1", Content: "new content"})

	block, ok := idx.Get("1")
	if !ok {
		t.Fatal("block '1' should exist")
	}
	if block.Content != "new content" {
		t.Errorf("expected 'new content', got %q", block.Content)
	}

	// Old token should be removed from inverted index
	if _, exists := idx.inverted["old"]; exists {
		t.Error("token 'old' should have been replaced by 'new'")
	}
	if _, exists := idx.inverted["new"]; !exists {
		t.Error("token 'new' should be in inverted index")
	}
}

func TestHybridIndex_Get_NotFound(t *testing.T) {
	idx := NewHybridIndex()
	_, ok := idx.Get("nonexistent")
	if ok {
		t.Error("expected false for nonexistent block")
	}
}

func TestHybridIndex_Remove(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello world"})
	idx.Add(&types.ContextBlock{ID: "2", Content: "hello foo"})

	idx.Remove("1")

	_, ok := idx.Get("1")
	if ok {
		t.Error("block '1' should have been removed")
	}

	// block '2' should still exist
	_, ok = idx.Get("2")
	if !ok {
		t.Error("block '2' should still exist")
	}

	// 'world' token should be gone (only block 1 had it)
	if _, exists := idx.inverted["world"]; exists {
		t.Error("token 'world' should have been removed from inverted index")
	}

	// 'hello' token should still exist (block 2 also has it)
	if _, exists := idx.inverted["hello"]; !exists {
		t.Error("token 'hello' should still be in inverted index")
	}
}

func TestHybridIndex_Remove_Nonexistent(t *testing.T) {
	idx := NewHybridIndex()
	// Should not panic
	idx.Remove("nonexistent")
}

func TestHybridIndex_MarkDirty(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "test", Dirty: false})

	idx.MarkDirty("1")
	block, ok := idx.Get("1")
	if !ok {
		t.Fatal("block '1' should exist")
	}
	if !block.Dirty {
		t.Error("block should be marked dirty")
	}
}

func TestHybridIndex_MarkDirty_Nonexistent(t *testing.T) {
	idx := NewHybridIndex()
	// Should not panic
	idx.MarkDirty("nonexistent")
}

func TestHybridIndex_ApplyDecay(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "a", Score: 100})
	idx.Add(&types.ContextBlock{ID: "2", Content: "b", Score: 50})

	idx.ApplyDecay(0.5)

	b1, _ := idx.Get("1")
	b2, _ := idx.Get("2")

	if b1.Score != 50 {
		t.Errorf("expected score 50 after 0.5 decay, got %v", b1.Score)
	}
	if b2.Score != 25 {
		t.Errorf("expected score 25 after 0.5 decay, got %v", b2.Score)
	}
}

func TestHybridIndex_ApplyDecay_EmptyIndex(t *testing.T) {
	idx := NewHybridIndex()
	// Should not panic
	idx.ApplyDecay(0.5)
}

// --- Query ---

func TestHybridIndex_Query_ExactTokenMatch(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello world"})
	idx.Add(&types.ContextBlock{ID: "2", Content: "goodbye world"})
	idx.Add(&types.ContextBlock{ID: "3", Content: "nothing here"})

	results := idx.Query("hello", nil)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "1" {
		t.Errorf("expected block '1', got %q", results[0].ID)
	}
}

func TestHybridIndex_Query_MultipleTokenMatch(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello world"})
	idx.Add(&types.ContextBlock{ID: "2", Content: "hello universe"})
	idx.Add(&types.ContextBlock{ID: "3", Content: "hello world again"})

	results := idx.Query("hello world", nil)
	// Block 1 and 3 both match both tokens, block 2 only matches 'hello'
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}
}

func TestHybridIndex_Query_EmptyKeyword(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello"})
	idx.Add(&types.ContextBlock{ID: "2", Content: "world"})

	results := idx.Query("", nil)
	if len(results) != 2 {
		t.Fatalf("expected all 2 blocks for empty query, got %d", len(results))
	}
}

func TestHybridIndex_Query_NoMatch(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello world"})

	results := idx.Query("xyzzy", nil)
	if results != nil {
		t.Errorf("expected nil for no match, got %v", results)
	}
}

func TestHybridIndex_Query_MetadataFilterByID(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello"})
	idx.Add(&types.ContextBlock{ID: "2", Content: "hello"})

	results := idx.Query("hello", map[string]string{"id": "1"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "1" {
		t.Errorf("expected block '1', got %q", results[0].ID)
	}
}

func TestHybridIndex_Query_MetadataFilterByContent(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello world"})
	idx.Add(&types.ContextBlock{ID: "2", Content: "hello there"})

	results := idx.Query("hello", map[string]string{"content_contains": "world"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", results[0].Content)
	}
}

func TestHybridIndex_Query_MetadataFilterEmpty(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "1", Content: "hello"})

	results := idx.Query("hello", map[string]string{})
	if len(results) != 1 {
		t.Errorf("expected 1 result for empty metadata, got %d", len(results))
	}
}

// --- matchesMetadata ---

func TestMatchesMetadata_Empty(t *testing.T) {
	block := &types.ContextBlock{ID: "1", Content: "test"}
	if !matchesMetadata(block, nil) {
		t.Error("should match with nil metadata")
	}
	if !matchesMetadata(block, map[string]string{}) {
		t.Error("should match with empty metadata")
	}
}

func TestMatchesMetadata_IDMatch(t *testing.T) {
	block := &types.ContextBlock{ID: "abc", Content: "test"}
	if !matchesMetadata(block, map[string]string{"id": "abc"}) {
		t.Error("should match same ID")
	}
	if matchesMetadata(block, map[string]string{"id": "xyz"}) {
		t.Error("should not match different ID")
	}
}

func TestMatchesMetadata_ContentContains(t *testing.T) {
	block := &types.ContextBlock{ID: "1", Content: "hello world"}
	if !matchesMetadata(block, map[string]string{"content_contains": "hello"}) {
		t.Error("should match when content contains substring")
	}
	if matchesMetadata(block, map[string]string{"content_contains": "xyz"}) {
		t.Error("should not match when content does not contain substring")
	}
}

func TestMatchesMetadata_MultipleFilters(t *testing.T) {
	block := &types.ContextBlock{ID: "abc", Content: "hello world"}
	if !matchesMetadata(block, map[string]string{"id": "abc", "content_contains": "hello"}) {
		t.Error("should match when all filters pass")
	}
	if matchesMetadata(block, map[string]string{"id": "abc", "content_contains": "xyz"}) {
		t.Error("should not match when any filter fails")
	}
}
