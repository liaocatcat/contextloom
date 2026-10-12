package harness

import (
	"fmt"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// BenchmarkUnindexTokens10000 超大索引删除单块性能基线
// （反向映射优化前：O(全部词表) 扫描；优化后：O(该块 token 数)）。
func BenchmarkUnindexTokens10000(b *testing.B) {
	idx := NewHybridIndex()
	// 1 万块，每块 200 token（词表约 200 万 token 条目）
	for i := 0; i < 10000; i++ {
		blocks := make([]string, 0, 200)
		for j := 0; j < 200; j++ {
			blocks = append(blocks, fmt.Sprintf("tok%d_%d", i, j))
		}
		content := "shared "
		for _, t := range blocks {
			content += t + " "
		}
		idx.Add(&types.ContextBlock{ID: fmt.Sprintf("b%d", i), Content: content})
	}
	idx.Add(&types.ContextBlock{ID: "victim", Content: "victimtoken one two three"})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Add(&types.ContextBlock{ID: "victim", Content: fmt.Sprintf("victimtoken%d x y", i)}) // 覆盖写触发 unindex+index
	}
}
