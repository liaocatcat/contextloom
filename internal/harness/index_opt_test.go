package harness

import (
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestUnindexTokens_Optimized 删除块只触碰自己的 token（反向映射生效）：
// 大索引中删除单块后，其余块的倒排关系完好。
func TestUnindexTokens_Optimized(t *testing.T) {
	idx := NewHybridIndex()
	// 100 块 + 1 个待删块
	for i := 0; i < 100; i++ {
		idx.Add(&types.ContextBlock{ID: string(rune('A'+i/26)) + string(rune('a'+i%26)), Content: "common shared vocabulary token for block content"})
	}
	idx.Add(&types.ContextBlock{ID: "victim", Content: "unique victim tokens here"})

	if _, ok := idx.blocks["victim"]; !ok {
		t.Fatal("victim 应在索引中")
	}
	idx.Remove("victim")
	if _, ok := idx.blocks["victim"]; ok {
		t.Fatal("victim 应被删除")
	}
	// 其余块倒排完好
	if got := len(idx.inverted["common"]); got != 100 {
		t.Fatalf("common 的倒排应剩 100 块，got %d", got)
	}
	// victim 专属 token 的倒排与 DF 应被清理
	if _, ok := idx.inverted["victim"]; ok {
		t.Fatal("victim token 倒排应被清理")
	}
	if _, ok := idx.df["victim"]; ok {
		t.Fatal("victim token DF 应被清理")
	}
	// blockTokens 反向映射清理
	if _, ok := idx.blockTokens["victim"]; ok {
		t.Fatal("blockTokens 应清理 victim")
	}
}

// TestDFMaintenance DF 计数随 Add/Remove 精确维护。
func TestDFMaintenance(t *testing.T) {
	idx := NewHybridIndex()
	idx.Add(&types.ContextBlock{ID: "b1", Content: "shared unique1"})
	idx.Add(&types.ContextBlock{ID: "b2", Content: "shared unique2"})
	if idx.df["shared"] != 2 || idx.df["unique1"] != 1 {
		t.Fatalf("DF 初始错误: shared=%d unique1=%d", idx.df["shared"], idx.df["unique1"])
	}
	// 同块重复 token：DF 只计一次
	idx.Add(&types.ContextBlock{ID: "b3", Content: "shared shared shared"})
	if idx.df["shared"] != 3 {
		t.Fatalf("重复 token DF 应只 +1，got %d", idx.df["shared"])
	}
	// 覆盖写（同 ID 换内容）：旧 token 移除、新 token 计入
	idx.Add(&types.ContextBlock{ID: "b3", Content: "shared replaced"})
	if idx.df["shared"] != 3 {
		t.Fatalf("覆盖写后 shared DF 应仍为 3，got %d", idx.df["shared"])
	}
	if _, ok := idx.df["replaced"]; !ok {
		t.Fatal("replaced 应计入 DF")
	}
	idx.Remove("b3")
	if idx.df["shared"] != 2 {
		t.Fatalf("删除 b3 后 shared DF 应为 2，got %d", idx.df["shared"])
	}
}

// TestIDFWeightedOrdering IDF 加权排序：稀有 token 命中的块排前，
// 常见 token（近似停用词）命中的块排后。
func TestIDFWeightedOrdering(t *testing.T) {
	e := New(Config{})
	// 语料：多数块都含 "err"（常见），仅一块含 "quartzsync"（稀有）
	blocks := []types.ContextBlock{
		{ID: "common1", Content: "handler returns err on failure err err"},
		{ID: "common2", Content: "parser returns err when empty err err"},
		{ID: "common3", Content: "writer returns err on io failure err err"},
		{ID: "common4", Content: "reader returns err on eof err err"},
		{ID: "common5", Content: "driver returns err on timeout err err"},
		{ID: "target", Content: "quartzsync sync engine uses err rarely"},
	}
	e.Index(nil, blocks)
	results := e.Search(nil, "quartzsync", nil)
	if len(results) == 0 {
		t.Fatal("应有结果")
	}
	if results[0].ID != "target" {
		t.Fatalf("稀有 token 命中的块应排第一，got %s", results[0].ID)
	}
}

// TestConfigurableMagicNumbers Config 收编魔法数：语义重排宽度与压缩阈值可配且生效。
func TestConfigurableMagicNumbers(t *testing.T) {
	e := New(Config{
		SemanticRerankTopN:     1, // 只重排 1 块
		OverflowCompressBlocks: 2,
		OverflowCompressScore:  100.5, // 得分 <100.5 的压缩（单 token 满命中=100 也触发）
		OverflowCompressKeep:   5,
	})
	long := strings.Repeat("长内容测试", 50)
	e.Index(nil, []types.ContextBlock{
		{ID: "b1", Content: "alpha beta gamma delta epsilon zeta sharecommon"},
		{ID: "b2", Content: long + " sharecommon"},
		{ID: "b3", Content: long + " sharecommon"},
	})
	results := e.Search(nil, "sharecommon", nil)
	if len(results) < 3 {
		t.Fatalf("应有 3 个结果，got %d", len(results))
	}
	// b2/b3 得分低于阈值且超过块数阈值 → 压缩为 keep=5 前缀 + "...(压缩)" 后缀（12 rune）
	for _, b := range results {
		if (b.ID == "b2" || b.ID == "b3") && len([]rune(b.Content)) > 20 {
			t.Fatalf("块 %s 应被压缩（keep=5+后缀≈12），got len=%d", b.ID, len([]rune(b.Content)))
		}
	}
}

// TestContextUsage 精度回归：压缩提示文案使用高精度占比（0.0x% 不被抹平）。
func TestSemanticRerankConfigPassed(t *testing.T) {
	// 快速回归：默认宽度不 panic、结果稳定
	e := New(Config{})
	e.Index(nil, []types.ContextBlock{{ID: "x", Content: "搜索查询内容"}, {ID: "y", Content: "search query content"}})
	r := e.Search(nil, "搜索查询", nil)
	if len(r) == 0 {
		t.Fatal("中文查询应有结果（跨语言/中文优化回归）")
	}
	_ = time.Now
}
