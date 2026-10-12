package harness

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestNew_Engine(t *testing.T) {
	cfg := Config{
		ScoreThreshold: 60,
		DecayFactor:    0.8,
		AnchorRatio:    0.25,
	}
	eng := New(cfg)

	if eng == nil {
		t.Fatal("New returned nil")
	}
	if eng.index == nil {
		t.Error("index should not be nil")
	}
	if eng.scorer == nil {
		t.Error("scorer should not be nil")
	}
	if eng.window == nil {
		t.Error("window should not be nil")
	}
	if eng.cfg.ScoreThreshold != 60 {
		t.Errorf("expected ScoreThreshold 60, got %f", eng.cfg.ScoreThreshold)
	}
	if eng.cfg.DecayFactor != 0.8 {
		t.Errorf("expected DecayFactor 0.8, got %f", eng.cfg.DecayFactor)
	}
	if eng.cfg.AnchorRatio != 0.25 {
		t.Errorf("expected AnchorRatio 0.25, got %f", eng.cfg.AnchorRatio)
	}
	// Verify scorer threshold was set from config
	if eng.scorer.Threshold() != types.Score(60) {
		t.Errorf("expected scorer threshold 60, got %v", eng.scorer.Threshold())
	}
}

func TestEngine_Index_SingleBlock(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	blocks := []types.ContextBlock{
		{ID: "1", Content: "hello world", Score: 80},
	}
	eng.Index(ctx, blocks)

	_, ok := eng.index.Get("1")
	if !ok {
		t.Error("block should be in index after Index()")
	}
}

func TestEngine_Index_MultipleBlocks(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	blocks := []types.ContextBlock{
		{ID: "1", Content: "hello world"},
		{ID: "2", Content: "foo bar"},
		{ID: "3", Content: "baz qux"},
	}
	eng.Index(ctx, blocks)

	for _, b := range blocks {
		if _, ok := eng.index.Get(b.ID); !ok {
			t.Errorf("block %q should be in index", b.ID)
		}
	}
}

func TestEngine_Search_ReturnsResults(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "1", Content: "hello world", LastUsed: time.Now()},
		{ID: "2", Content: "goodbye universe", LastUsed: time.Now()},
		{ID: "3", Content: "hello there", LastUsed: time.Now()},
	})

	results := eng.Search(ctx, "hello", nil)
	if len(results) == 0 {
		t.Fatal("expected results for 'hello' query")
	}

	// Results should include blocks with 'hello'
	found := false
	for _, r := range results {
		if r.ID == "1" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected block '1' (hello world) in results")
	}
}

func TestEngine_Search_WithMetadata(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "alpha", Content: "hello world", LastUsed: time.Now()},
		{ID: "beta", Content: "hello there", LastUsed: time.Now()},
	})

	results := eng.Search(ctx, "hello", map[string]string{"id": "alpha"})
	if len(results) != 1 {
		t.Fatalf("expected 1 result with id filter, got %d", len(results))
	}
	if results[0].ID != "alpha" {
		t.Errorf("expected 'alpha', got %q", results[0].ID)
	}
}

func TestEngine_Search_NoMatch(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "1", Content: "hello world", LastUsed: time.Now()},
	})

	results := eng.Search(ctx, "nonexistent", nil)
	// Should return nil or empty for no match
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestEngine_Search_AppliesScoring(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "1", Content: "hello world", LastUsed: time.Now()},
		{ID: "2", Content: "hello there universe", LastUsed: time.Now()},
	})

	results := eng.Search(ctx, "hello world", nil)

	// Scores should have been applied
	for _, r := range results {
		if r.Score == 0 {
			t.Errorf("block %q has score 0, expected >0", r.ID)
		}
	}
}

func TestEngine_Search_ReturnsWindowOrder(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 20, DecayFactor: 0.9, AnchorRatio: 0.3})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "1", Content: "aaa", LastUsed: time.Now()},
		{ID: "2", Content: "bbb", LastUsed: time.Now()},
		{ID: "3", Content: "ccc", LastUsed: time.Now()},
	})

	results := eng.Search(ctx, "aaa bbb ccc", nil)
	// Results should be sorted by score descending (window Arrange sorts before returning)
	for i := 1; i < len(results); i++ {
		if results[i-1].Score < results[i].Score {
			t.Errorf("results not sorted descending: [%d]=%v, [%d]=%v",
				i-1, results[i-1].Score, i, results[i].Score)
		}
	}
}

func TestEngine_Rerank(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "1", Content: "hello world", Dirty: false},
	})

	eng.Rerank(ctx, "1")
	block, ok := eng.index.Get("1")
	if !ok {
		t.Fatal("block '1' should exist")
	}
	if !block.Dirty {
		t.Error("block should be marked dirty after Rerank")
	}
}

func TestEngine_Rerank_Nonexistent(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	// Should not panic
	eng.Rerank(ctx, "nonexistent")
}

func TestEngine_Invalidate(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "1", Content: "hello world"},
		{ID: "2", Content: "foo bar"},
	})

	eng.Invalidate("1")

	_, ok := eng.index.Get("1")
	if ok {
		t.Error("block '1' should be removed after Invalidate")
	}

	_, ok = eng.index.Get("2")
	if !ok {
		t.Error("block '2' should still exist")
	}
}

func TestEngine_Invalidate_Nonexistent(t *testing.T) {
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	// Should not panic
	eng.Invalidate("nonexistent")
}

func TestEngine_SetThreshold(t *testing.T) {
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.SetThreshold(80)
	if eng.cfg.ScoreThreshold != 80 {
		t.Errorf("expected config threshold 80, got %f", eng.cfg.ScoreThreshold)
	}
	if eng.scorer.Threshold() != 80 {
		t.Errorf("expected scorer threshold 80, got %v", eng.scorer.Threshold())
	}
}

func TestEngine_ApplyDecay(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.5, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "1", Content: "hello", Score: 100},
	})

	eng.ApplyDecay()

	block, _ := eng.index.Get("1")
	if block.Score != 50 {
		t.Errorf("expected score 50 after decay, got %v", block.Score)
	}
}

func TestEngine_Concurrency(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	// Index blocks concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			blocks := []types.ContextBlock{
				{
					ID:       string(rune('a' + id)),
					Content:  "test content",
					LastUsed: time.Now(),
				},
			}
			eng.Index(ctx, blocks)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	// Search concurrently
	for i := 0; i < 10; i++ {
		go func() {
			_ = eng.Search(ctx, "test", nil)
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestEngine_FullLifecycle(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{
		ScoreThreshold: 30,
		DecayFactor:    0.8,
		AnchorRatio:    0.2,
	})

	// Index
	eng.Index(ctx, []types.ContextBlock{
		{ID: "session-1", Content: "user authentication module", LastUsed: time.Now()},
		{ID: "session-2", Content: "database connection pool", LastUsed: time.Now()},
		{ID: "session-3", Content: "http request handler", LastUsed: time.Now()},
		{ID: "session-4", Content: "json serialization utils", LastUsed: time.Now()},
	})

	// Search for authentication-related
	results := eng.Search(ctx, "authentication", nil)
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'authentication'")
	}
	if results[0].ID != "session-1" {
		t.Errorf("expected 'session-1' as top result, got %q", results[0].ID)
	}

	// Rerank another block
	eng.Rerank(ctx, "session-2")

	// Invalidate one
	eng.Invalidate("session-4")
	_, ok := eng.index.Get("session-4")
	if ok {
		t.Error("session-4 should be invalidated")
	}

	// Apply decay
	eng.ApplyDecay()

	// Set new threshold
	eng.SetThreshold(70)

	// Search should still work
	results2 := eng.Search(ctx, "database", nil)
	if len(results2) == 0 {
		t.Fatal("expected results for 'database' after lifecycle operations")
	}
}

// ── 会话隔离（防同项目跨会话串扰）──

func TestEngine_Search_SessionIsolation(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	// 项目级共享块（SessionID 空）：所有会话可见
	// 会话 A 私有块、会话 B 私有块
	eng.Index(ctx, []types.ContextBlock{
		{ID: "shared-1", Content: "项目配置说明文档", Score: 80},
		{ID: "a-1", Content: "[压缩原文 assistant] A会话讨论登录功能设计方案", Score: 50, SessionID: "session-A"},
		{ID: "b-1", Content: "[压缩原文 assistant] B会话讨论支付网关对接细节", Score: 50, SessionID: "session-B"},
	})

	// A 会话检索：只能看到共享块 + A 的私有块，不能看到 B 的
	resA := eng.Search(ctx, "登录功能支付网关", map[string]string{"session_id": "session-A"})
	for _, b := range resA {
		if b.ID == "b-1" {
			t.Errorf("会话 A 检索到了会话 B 的私有块 b-1: %q", b.Content)
		}
	}
	foundA := false
	for _, b := range resA {
		if b.ID == "a-1" {
			foundA = true
		}
	}
	if !foundA {
		t.Error("会话 A 应能检索到自己的私有块 a-1")
	}

	// B 会话：不能看到 A 的私有块
	resB := eng.Search(ctx, "登录支付", map[string]string{"session_id": "session-B"})
	for _, b := range resB {
		if b.ID == "a-1" {
			t.Errorf("会话 B 检索到了会话 A 的私有块: %q", b.Content)
		}
	}

	// 无过滤（旧行为/未绑定会话）：共享块可见
	resFree := eng.Search(ctx, "项目文档", nil)
	foundShared := false
	for _, b := range resFree {
		if b.ID == "shared-1" {
			foundShared = true
		}
	}
	if !foundShared {
		t.Error("共享块应对所有检索可见")
	}
}

func TestSearch_CacheKey_SessionScoped(t *testing.T) {
	ctx := context.Background()
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})

	eng.Index(ctx, []types.ContextBlock{
		{ID: "a-1", Content: "会话A的登录实现细节", Score: 80, SessionID: "session-A"},
		{ID: "b-1", Content: "会话B的登录实现细节", Score: 80, SessionID: "session-B"},
	})

	// 先 A 后 B：若缓存键不含会话 ID，B 会命中 A 的缓存（顺序敏感）
	resA := eng.Search(ctx, "登录", map[string]string{"session_id": "session-A"})
	resB := eng.Search(ctx, "登录", map[string]string{"session_id": "session-B"})

	// 同 query 但是 B 会话，绝不能返回 A 的私有块
	for _, b := range resB {
		if b.ID == "a-1" {
			t.Fatalf("缓存/检索串会话：会话 B 检索到 A 的私有块 %q", b.Content)
		}
	}
	// A 会话结果应包含 a-1
	for _, b := range resA {
		if b.ID == "a-1" {
			return // ok
		}
	}
	t.Error("会话 A 应检索到自身私有块 a-1")
}

func TestSessionID_PersistRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.json")
	eng := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	eng.Index(context.Background(), []types.ContextBlock{
		{ID: "a", Content: "A会话私有原文", Score: 50, SessionID: "sess-A"},
		{ID: "shared", Content: "共享文档", Score: 60},
	})
	if err := eng.SaveIndex(path); err != nil {
		t.Fatal(err)
	}
	eng2 := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	if err := eng2.LoadIndex(path); err != nil {
		t.Fatal(err)
	}
	// 加载后会话过滤仍生效
	res := eng2.Search(context.Background(), "私有压缩", map[string]string{"session_id": "sess-A"})
	if len(res) == 0 {
		t.Fatal("会话 A 私有块应可检索")
	}
	resB := eng2.Search(context.Background(), "私有压缩", map[string]string{"session_id": "sess-B"})
	for _, b := range resB {
		if b.ID == "a" {
			t.Fatal("B 会话不应搜到 A 私有块")
		}
	}
}
