package harness

import (
	"regexp"
	"strings"
	"sync"
)

// SymbolIndex 轻量符号索引（对标业界前沿 repo map / 符号级检索的零依赖实现）。
//
// 原理：用正则提取常见语言（Go/Python/JS/TS）的函数、类型、常量、变量、类名，
// 建立 符号名(小写) → blockID 集合 的映射。检索时若查询命中符号名（精确或前缀），
// 对应 block 在 Search 中加权——弥补纯关键词倒排索引对"语义级代码库理解"的短板，
// 且零依赖、纯内存、不引入 embedding/tree-sitter 等重资产。
type SymbolIndex struct {
	mu  sync.RWMutex
	idx map[string]map[string]struct{} // symbol(小写) -> blockID set
}

// symbolPatterns 多语言符号提取正则（顺序无关，逐条匹配去重）。
var symbolPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bfunc\s+(\w+)`),             // Go 函数
	regexp.MustCompile(`\btype\s+(\w+)`),             // Go 类型
	regexp.MustCompile(`\bconst\s+(\w+)\s*=`),        // Go/JS 常量
	regexp.MustCompile(`\bvar\s+(\w+)\s*=`),          // Go/JS 变量
	regexp.MustCompile(`\bdef\s+(\w+)`),              // Python 函数
	regexp.MustCompile(`\bclass\s+(\w+)`),            // Python/JS 类
	regexp.MustCompile(`\bfunction\s+(\w+)`),         // JS 函数
	regexp.MustCompile(`\bstruct\s+(\w+)`),           // Go 结构体（type X struct 双命中）
	regexp.MustCompile(`\binterface\s+(\w+)`),        // Go 接口
	regexp.MustCompile(`\bpub(?:lic)?\s+fn\s+(\w+)`), // Rust
}

func NewSymbolIndex() *SymbolIndex {
	return &SymbolIndex{idx: make(map[string]map[string]struct{})}
}

// ExtractSymbols 从内容中提取符号名（小写去重）。空内容返回 nil。
func ExtractSymbols(content string) []string {
	if content == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, re := range symbolPatterns {
		for _, m := range re.FindAllStringSubmatch(content, -1) {
			if len(m) < 2 {
				continue
			}
			s := strings.ToLower(m[1])
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// Add 将内容中的符号名关联到 blockID。
func (s *SymbolIndex) Add(content, blockID string) {
	syms := ExtractSymbols(content)
	if len(syms) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sym := range syms {
		set, ok := s.idx[sym]
		if !ok {
			set = make(map[string]struct{})
			s.idx[sym] = set
		}
		set[blockID] = struct{}{}
	}
}

// Lookup 查询符号命中的 blockID 集合。
// 匹配规则：query 小写后先精确匹配；再取长度 ≥2 的前缀匹配（最多 3 个符号名），
// 覆盖"记得函数名前半段"的模糊查询场景。
func (s *SymbolIndex) Lookup(query string) map[string]struct{} {
	q := strings.ToLower(strings.TrimSpace(query))
	if len(q) == 0 {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	hits := make(map[string]struct{})
	if set, ok := s.idx[q]; ok {
		for id := range set {
			hits[id] = struct{}{}
		}
	}
	// 前缀匹配（仅当 query 无精确命中或长度足够时，最多 3 个符号）
	if len(q) >= 2 {
		count := 0
		for sym, set := range s.idx {
			if strings.HasPrefix(sym, q) && sym != q {
				for id := range set {
					hits[id] = struct{}{}
				}
				count++
				if count >= 3 {
					break
				}
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return hits
}

// Clear 清空索引（压缩审计重置等场景使用）。
func (s *SymbolIndex) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idx = make(map[string]map[string]struct{})
}
