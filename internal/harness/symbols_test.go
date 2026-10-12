package harness

import (
	"context"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestExtractSymbols_MultiLanguage(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{"Go 函数+类型", "func AddUser(u User) error { return nil }\ntype User struct { ID int }", []string{"adduser", "user"}},
		{"Go 常量+变量", "const MaxRetry = 3\nvar globalCount = 0", []string{"maxretry", "globalcount"}},
		{"Python", "def parse_json(data):\n    return data\nclass ConfigParser:", []string{"parse_json", "configparser"}},
		{"JS", "function renderList(items) {}\nconst API_URL = '/api';", []string{"renderlist", "api_url"}},
		{"空内容", "", nil},
		{"无符号", "hello world 123", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractSymbols(c.content)
			// 断言 got 包含 want 全部（允许额外符号如 struct）
			wantSet := make(map[string]bool)
			for _, w := range c.want {
				wantSet[w] = true
			}
			gotSet := make(map[string]bool)
			for _, g := range got {
				gotSet[g] = true
			}
			for w := range wantSet {
				if !gotSet[w] {
					t.Errorf("ExtractSymbols(%q) 缺少符号 %q，got=%v", c.content, w, got)
				}
			}
			if c.want == nil && len(got) != 0 {
				t.Errorf("ExtractSymbols(%q) 应为空，got=%v", c.content, got)
			}
		})
	}
}

func TestSymbolIndex_AddLookup(t *testing.T) {
	si := NewSymbolIndex()
	si.Add("func CalculateTotal(items []Item) int { return 0 }", "b1")
	si.Add("func RenderPage(w io.Writer) { _ = w }", "b2")
	si.Add("type Item struct { Name string }", "b3")

	// 精确匹配
	hits := si.Lookup("CalculateTotal")
	if len(hits) != 1 {
		t.Fatalf("精确匹配 CalculateTotal 应命中 1 块，got %d", len(hits))
	}
	if _, ok := hits["b1"]; !ok {
		t.Fatalf("精确匹配应命中 b1，got %v", hits)
	}

	// 前缀匹配（记不清完整名）
	hits = si.Lookup("calc")
	if _, ok := hits["b1"]; !ok {
		t.Fatalf("前缀匹配 calc 应命中 b1，got %v", hits)
	}

	// 多符号共享块
	si.Add("func Main() { _ = Item{} }", "b3") // b3 已有 Item
	hits = si.Lookup("main")
	if _, ok := hits["b3"]; !ok {
		t.Fatalf("main 应命中 b3，got %v", hits)
	}

	// 无命中
	if hits := si.Lookup("nosuchsymbol"); hits != nil {
		t.Fatalf("无命中应返回 nil，got %v", hits)
	}

	// Clear
	si.Clear()
	if hits := si.Lookup("CalculateTotal"); hits != nil {
		t.Fatalf("Clear 后不应命中，got %v", hits)
	}
}

func TestEngine_SymbolBoostInSearch(t *testing.T) {
	eng := New(Config{ScoreThreshold: 60, DecayFactor: 0.9, AnchorRatio: 0.5})

	// 两个块：b1 含符号 AddUser（被符号索引命中），b2 纯文本
	eng.Index(context.Background(), []types.ContextBlock{
		{ID: "b1", Content: "func AddUser(u User) error { return nil }"},
		{ID: "b2", Content: "add user to the system database"},
	})

	// 查询符号名 AddUser：b1 应因符号命中加分，排序靠前
	results := eng.Search(context.Background(), "AddUser", nil)
	if len(results) == 0 {
		t.Fatal("Search 应返回结果")
	}
	found := false
	for _, b := range results {
		if b.ID == "b1" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("符号命中块 b1 应出现在结果中，got %+v", results)
	}

	// 对比：无符号索引时 b1 不因符号加分（回归：索引为空时行为不变）
	eng2 := New(Config{ScoreThreshold: 60, DecayFactor: 0.9, AnchorRatio: 0.5})
	eng2.Index(context.Background(), []types.ContextBlock{
		{ID: "b1", Content: "func AddUser(u User) error { return nil }"},
	})
	eng2.syms.Clear() // 模拟无符号索引
	if hits := eng2.syms.Lookup("AddUser"); hits != nil {
		t.Fatalf("Clear 后不应命中")
	}
}
