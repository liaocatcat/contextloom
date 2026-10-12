package harness

import (
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestSearchCache_LRU_Eviction 验证 LRU 容量上限：插入超容量后淘汰队尾最久未用。
func TestSearchCache_LRU_Eviction(t *testing.T) {
	c := newSearchCache(3, time.Minute)
	for _, k := range []string{"k1", "k2", "k3"} {
		c.set(k, []types.ContextBlock{{ID: k}})
	}
	// 触摸 k1（变为最近使用）
	if _, ok := c.get("k1"); !ok {
		t.Fatal("k1 应命中")
	}
	// 插入 k4 → 淘汰最久未用的 k2（不是 k1）
	c.set("k4", []types.ContextBlock{{ID: "k4"}})
	if _, ok := c.get("k2"); ok {
		t.Fatal("k2 应被 LRU 淘汰")
	}
	for _, k := range []string{"k1", "k3", "k4"} {
		if _, ok := c.get(k); !ok {
			t.Fatalf("%s 不应被淘汰", k)
		}
	}
	if c.len() != 3 {
		t.Fatalf("容量应保持 3，got %d", c.len())
	}
}

// TestSearchCache_TTL_Expire 验证 TTL 过期惰性淘汰。
func TestSearchCache_TTL_Expire(t *testing.T) {
	c := newSearchCache(4, 20*time.Millisecond)
	c.set("a", []types.ContextBlock{{ID: "a"}})
	if _, ok := c.get("a"); !ok {
		t.Fatal("写入后立即可命中")
	}
	time.Sleep(30 * time.Millisecond)
	if _, ok := c.get("a"); ok {
		t.Fatal("TTL 过期后不应命中")
	}
	if c.len() != 0 {
		t.Fatalf("过期条目应被清除，got %d", c.len())
	}
}

// TestSearchCache_EngineScoped 验证缓存归属 Engine 实例：两引擎同 query 不串扰。
func TestSearchCache_EngineScoped(t *testing.T) {
	e1 := New(Config{})
	e2 := New(Config{})
	b1 := []types.ContextBlock{{ID: "e1-block", Content: "alpha engine one"}}
	b2 := []types.ContextBlock{{ID: "e2-block", Content: "beta engine two"}}
	e1.Index(nil, b1)
	e2.Index(nil, b2)
	r1 := e1.Search(nil, "alpha", nil)
	r2 := e2.Search(nil, "alpha", nil)
	if len(r1) == 0 || r1[0].ID != "e1-block" {
		t.Fatalf("e1 应命中自己的块，got %+v", r1)
	}
	// e2 无 "alpha" 相关块——若共享缓存会错误命中 e1 结果
	for _, b := range r2 {
		if b.ID == "e1-block" {
			t.Fatal("引擎间缓存串扰：e2 命中了 e1 的缓存结果")
		}
	}
}

// TestEngine_Index_InvalidatesCache 索引变更清缓存。
func TestEngine_Index_InvalidatesCache(t *testing.T) {
	e := New(Config{})
	e.Index(nil, []types.ContextBlock{{ID: "a", Content: "hello world content"}})
	first := e.Search(nil, "hello", nil)
	if len(first) == 0 {
		t.Fatal("应命中")
	}
	// 叠加新块后同查询应能看到新块（缓存已被 Index 清空）
	e.Index(nil, []types.ContextBlock{{ID: "b", Content: "hello again content"}})
	second := e.Search(nil, "hello", nil)
	found := false
	for _, b := range second {
		if b.ID == "b" {
			found = true
		}
	}
	if !found {
		t.Fatal("Index 后缓存未失效：新块未出现")
	}
}
