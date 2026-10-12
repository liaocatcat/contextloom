package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEvaluateRetrieval_Baseline Recall@K / MRR 评测基线冒烟：
// 中文查询应命中中文块，英文代码查询应命中英文块（中文/代码双优化回归）。
func TestEvaluateRetrieval_Baseline(t *testing.T) {
	spec := &RetrievalSpec{
		Name: "smoke",
		Corpus: []RetrievalBlock{
			{ID: "zh-cache", Content: "搜索缓存使用 LRU 淘汰策略，容量上限 256 条，过期时间 2 秒"},
			{ID: "zh-index", Content: "倒排索引维护 token 到块的映射，支持快速检索与删除"},
			{ID: "en-lru", Content: "searchCache implements LRU eviction with capacity limit 256"},
			{ID: "en-idf", Content: "IDF weighting downgrades common tokens like the and err"},
			{ID: "noise1", Content: "unrelated content about cooking pasta and pizza recipes"},
			{ID: "noise2", Content: "random notes about vacation planning in summer"},
		},
		Queries: []RetrievalQuery{
			{Query: "检索缓存容量淘汰", Gold: []string{"zh-cache"}},
			{Query: "search cache LRU capacity", Gold: []string{"en-lru"}},
			{Query: "倒排索引删除", Gold: []string{"zh-index"}},
			{Query: "IDF common tokens weighting", Gold: []string{"en-idf"}},
		},
	}
	rep, err := EvaluateRetrieval(spec)
	if err != nil {
		t.Fatalf("评测失败: %v", err)
	}
	if rep.Queries != 4 || rep.Corpus != 6 {
		t.Fatalf("规模不符: %+v", rep)
	}
	if rep.RecallK["R@1"] < 0.5 {
		t.Fatalf("R@1 过低: %.2f（详情 %+v）", rep.RecallK["R@1"], rep.Details)
	}
	if rep.MRR < 0.5 {
		t.Fatalf("MRR 过低: %.3f（详情 %+v）", rep.MRR, rep.Details)
	}
	t.Logf("基线: %s", rep.RenderText())
}

// TestRunRetrieval_Yaml 端到端：yaml 加载 → 评测 → 报告。
func TestRunRetrieval_Yaml(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "retrieval.yaml")
	content := `name: mini
corpus:
  - id: doc1
    content: "配置热更新支持检索参数与缓存容量"
  - id: doc2
    content: "数据库连接池管理最大连接数"
queries:
  - query: "配置热更新"
    gold: [doc1]
  - query: "连接池上限"
    gold: [doc2]
ks: [1, 3]
`
	if err := os.WriteFile(yamlPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := RunRetrieval(yamlPath)
	if err != nil {
		t.Fatalf("yaml 评测失败: %v", err)
	}
	if rep.Name != "mini" || rep.Queries != 2 {
		t.Fatalf("报告不符: %+v", rep)
	}
	if v, ok := rep.RecallK["R@1"]; !ok || v < 0.99 {
		t.Fatalf("R@1 应为 1.0, got %.2f", v)
	}
}
