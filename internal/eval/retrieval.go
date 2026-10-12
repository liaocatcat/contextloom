package eval

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/pkg/types"
	"gopkg.in/yaml.v3"
)

// 检索质量评测基线（Recall@K / MRR）——补齐"提升靠修 bug 驱动、没有数据基线"的短板。
//
// 用法：retrieval.yaml 定义查询 → 相关块全集（gold），引擎索引固定语料库后逐查询
// 检索，输出 Recall@K / MRR / 命中排名明细。纯离线零 API 依赖，CI 可复跑。

// RetrievalSpec 评测集定义（retrieval.yaml）。
type RetrievalSpec struct {
	Name    string           `yaml:"name"`
	Corpus  []RetrievalBlock `yaml:"corpus"`  // 索引语料库
	Queries []RetrievalQuery `yaml:"queries"` // 查询 + 相关块 ID 全集
	Ks      []int            `yaml:"ks"`      // Recall@K 的 K 列表（缺省 [1,3,5,10]）
}

// RetrievalBlock 语料块（Meta 预留：未来支持按会话/元数据过滤的检索评测）。
type RetrievalBlock struct {
	ID      string            `yaml:"id"`
	Content string            `yaml:"content"`
	Meta    map[string]string `yaml:"meta,omitempty"`
}

// RetrievalQuery 查询及其相关块（gold）。
type RetrievalQuery struct {
	Query string   `yaml:"query"`
	Gold  []string `yaml:"gold"` // 相关块 ID 全集
}

// RetrievalQueryResult 单查询结果。
type RetrievalQueryResult struct {
	Query  string  `json:"query"`
	Ranks  []int   `json:"ranks"` // 各 gold 块在结果中的排名（1-based，未命中=0）
	Recall float64 `json:"recall_at_k"`
	MRR    float64 `json:"mrr"`
}

// RetrievalReport 评测报告。
type RetrievalReport struct {
	Name    string                 `json:"name"`
	Queries int                    `json:"queries"`
	Corpus  int                    `json:"corpus"`
	Ks      []int                  `json:"ks"`
	RecallK map[string]float64     `json:"recall_at_k"` // "R@5" → 均值
	MRR     float64                `json:"mrr"`
	Details []RetrievalQueryResult `json:"details"`
}

// RunRetrieval 加载并执行检索评测（零 API 依赖，可 dry-run）。
func RunRetrieval(specPath string) (*RetrievalReport, error) {
	spec, err := LoadRetrievalSpec(specPath)
	if err != nil {
		return nil, err
	}
	return EvaluateRetrieval(spec)
}

// LoadRetrievalSpec 解析 retrieval.yaml。
func LoadRetrievalSpec(path string) (*RetrievalSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", path, err)
	}
	var spec RetrievalSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return &spec, nil
}

// EvaluateRetrieval 对固定语料建立引擎索引，逐查询检索并统计 Recall@K / MRR。
func EvaluateRetrieval(spec *RetrievalSpec) (*RetrievalReport, error) {
	if len(spec.Queries) == 0 {
		return nil, fmt.Errorf("评测集无查询")
	}
	eng := harness.New(harness.Config{})
	blocks := make([]types.ContextBlock, 0, len(spec.Corpus))
	for i := range spec.Corpus {
		blocks = append(blocks, types.ContextBlock{
			ID:      spec.Corpus[i].ID,
			Content: spec.Corpus[i].Content,
		})
	}
	eng.Index(context.Background(), blocks)

	ks := spec.Ks
	if len(ks) == 0 {
		ks = []int{1, 3, 5, 10}
	}

	rep := &RetrievalReport{
		Name: spec.Name, Queries: len(spec.Queries), Corpus: len(spec.Corpus),
		Ks: ks, RecallK: map[string]float64{},
	}
	sumRecall := map[int]float64{}
	sumMRR := 0.0
	for _, q := range spec.Queries {
		results := eng.Search(context.Background(), q.Query, nil)
		res := RetrievalQueryResult{Query: q.Query}
		goldSet := make(map[string]bool, len(q.Gold))
		for _, g := range q.Gold {
			goldSet[g] = true
		}
		// 各 gold 的排名
		rankOf := map[string]int{}
		for rank, r := range results {
			if goldSet[r.ID] {
				if _, seen := rankOf[r.ID]; !seen {
					rankOf[r.ID] = rank + 1
				}
			}
		}
		for _, g := range q.Gold {
			res.Ranks = append(res.Ranks, rankOf[g])
		}
		// Recall@K（取 K 列表中最大的 K；按 gold 命中比例）
		maxK := 0
		for _, k := range ks {
			if k > maxK {
				maxK = k
			}
		}
		hitsAtMaxK := 0
		for _, g := range q.Gold {
			if rankOf[g] > 0 && rankOf[g] <= maxK {
				hitsAtMaxK++
			}
		}
		if len(q.Gold) > 0 {
			res.Recall = float64(hitsAtMaxK) / float64(len(q.Gold))
		}
		// MRR：首个命中 gold 的倒数排名
		best := 0
		for _, r := range res.Ranks {
			if r > 0 && (best == 0 || r < best) {
				best = r
			}
		}
		if best > 0 {
			res.MRR = 1.0 / float64(best)
		}
		for _, k := range ks {
			hits := 0
			for _, g := range q.Gold {
				if rankOf[g] > 0 && rankOf[g] <= k {
					hits++
				}
			}
			if len(q.Gold) > 0 {
				sumRecall[k] += float64(hits) / float64(len(q.Gold))
			}
		}
		sumMRR += res.MRR
		rep.Details = append(rep.Details, res)
	}
	for _, k := range ks {
		rep.RecallK[fmt.Sprintf("R@%d", k)] = sumRecall[k] / float64(len(spec.Queries))
	}
	rep.MRR = sumMRR / float64(len(spec.Queries))
	return rep, nil
}

// RenderRetrievalText 报告文本渲染。
func (r *RetrievalReport) RenderText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "检索评测 %s：语料 %d 块 / 查询 %d 条\n", r.Name, r.Corpus, r.Queries)
	ks := append([]int(nil), r.Ks...)
	sort.Ints(ks)
	for _, k := range ks {
		fmt.Fprintf(&b, "  Recall@%d = %.2f\n", k, r.RecallK[fmt.Sprintf("R@%d", k)])
	}
	fmt.Fprintf(&b, "  MRR = %.3f\n", r.MRR)
	return b.String()
}
