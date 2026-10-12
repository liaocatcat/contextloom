package harness

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestEngine_SaveLoadIndex_RebuildsSymbols 验证 Engine 级持久化闭环：
// 保存后新引擎加载，倒排索引与符号索引都重建（Search 带符号加权命中）。
func TestEngine_SaveLoadIndex_RebuildsSymbols(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engine-index.json")

	e1 := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	e1.Index(context.Background(), []types.ContextBlock{
		{ID: "b1", Content: "func AlphaFunc 处理 alpha 逻辑", Score: 50, LastUsed: time.Now()},
		{ID: "b2", Content: "Beta 模块实现 beta 流程", Score: 50, LastUsed: time.Now()},
	})
	if !e1.NeedsSave() {
		t.Fatal("Index 后 NeedsSave 应为 true")
	}
	if err := e1.SaveIndex(path); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	if e1.NeedsSave() {
		t.Fatal("SaveIndex 后 NeedsSave 应为 false")
	}

	e2 := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	if err := e2.LoadIndex(path); err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}

	// 关键词命中（倒排重建）
	got := e2.Search(context.Background(), "alpha", nil)
	if len(got) == 0 || got[0].ID != "b1" {
		t.Errorf("Search(alpha) = %v, want 命中 b1", idsOf(got))
	}
	// 符号命中（syms 重建）
	got = e2.Search(context.Background(), "AlphaFunc", nil)
	if len(got) == 0 {
		t.Fatal("Search(AlphaFunc) 未命中——符号索引未重建")
	}
	if got[0].ID != "b1" {
		t.Errorf("Search(AlphaFunc) = %v, want b1", idsOf(got))
	}
	if got[0].Score < 50 {
		t.Errorf("符号命中应有加权: score=%v", got[0].Score)
	}
	// 加载后无未落盘变更
	if e2.NeedsSave() {
		t.Error("LoadIndex 后 NeedsSave 应为 false")
	}
}

func TestEngine_NeedsSave_TracksAllMutations(t *testing.T) {
	e := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	if e.NeedsSave() {
		t.Fatal("初始状态不应需要保存")
	}

	e.Index(context.Background(), []types.ContextBlock{{ID: "x", Content: "hello world"}})
	if !e.NeedsSave() {
		t.Fatal("Index 后应需要保存")
	}
	if err := e.SaveIndex(filepath.Join(t.TempDir(), "i.json")); err != nil {
		t.Fatal(err)
	}
	if e.NeedsSave() {
		t.Fatal("SaveIndex 后不应需要保存")
	}

	e.Invalidate("x")
	if !e.NeedsSave() {
		t.Fatal("Invalidate 后应需要保存")
	}
	if err := e.SaveIndex(filepath.Join(t.TempDir(), "i2.json")); err != nil {
		t.Fatal(err)
	}

	e.Index(context.Background(), []types.ContextBlock{{ID: "y", Content: "more"}})
	e.Rerank(context.Background(), "y")
	if !e.NeedsSave() {
		t.Fatal("Rerank 后应需要保存")
	}
}

func TestEngine_LoadIndex_MissingFile_NoError(t *testing.T) {
	e := New(Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	if err := e.LoadIndex(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("LoadIndex 不存在文件应返回 nil, got %v", err)
	}
}
