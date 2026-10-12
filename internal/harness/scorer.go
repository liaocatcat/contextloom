package harness

import (
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

type RelevanceScorer struct {
	threshold types.Score
}

func NewRelevanceScorer() *RelevanceScorer {
	return &RelevanceScorer{threshold: 50}
}

func (s *RelevanceScorer) Score(query string, block *types.ContextBlock) types.Score {
	if block == nil || query == "" {
		return 0
	}

	queryTokens := tokenize(query)
	blockTokens := tokenize(block.Content)

	if len(queryTokens) == 0 || len(blockTokens) == 0 {
		return 0
	}

	querySet := toSet(queryTokens)
	blockSet := toSet(blockTokens)

	intersection := 0
	for token := range querySet {
		if blockSet[token] {
			intersection++
		}
	}

	overlapScore := float64(intersection) / float64(len(querySet)) * 85

	recencyScore := 15.0
	if !block.LastUsed.IsZero() {
		hoursSinceUse := time.Since(block.LastUsed).Hours()
		if hoursSinceUse < 1 {
			recencyScore = 15
		} else if hoursSinceUse < 24 {
			recencyScore = 15 * (1 - hoursSinceUse/24)
		} else {
			recencyScore = 0
		}
	}

	score := overlapScore + recencyScore
	return types.Score(math.Min(100, math.Max(0, score)))
}

func tokenize(s string) []string {
	var tokens []string
	var current strings.Builder // 英文/代码 token 累积（字母+数字+下划线）
	var chineseRunes []rune     // 连续中文 rune 累积

	// flushChinese 对累积的中文生成单字 + bigram 组合，提高匹配精度。
	// 修复：之前中文按单字拆分（"总结项目特征"→6个单字），匹配极度发散、同义不同字零匹配。
	// 现在"总结"产生 ["总","总结","结","结项",...]，bigram 命中比单字更可靠。
	flushChinese := func() {
		if len(chineseRunes) == 0 {
			return
		}
		for i := 0; i < len(chineseRunes); i++ {
			tokens = append(tokens, string(chineseRunes[i])) // 单字兜底
			if i+1 < len(chineseRunes) {
				tokens = append(tokens, string(chineseRunes[i])+string(chineseRunes[i+1])) // bigram
			}
		}
		chineseRunes = nil
	}

	flushWord := func() {
		if current.Len() > 0 {
			w := current.String()
			tokens = append(tokens, strings.ToLower(w))
			// 驼峰/下划线子词拆分：SemanticRerankTopN → [semanticreranktopn(原样), semantic, rerank, top, n]。
			// 完整 token 保留在前（精确命中优先），子词使英文查询命中文档里的组件式标识符。
			var subs []string
			var cur []rune
			rs := []rune(w)
			flushSub := func() {
				if len(cur) > 0 {
					subs = append(subs, strings.ToLower(string(cur)))
					cur = cur[:0]
				}
			}
			for i, r := range rs {
				if r == '_' {
					flushSub()
					continue
				}
				if unicode.IsUpper(r) && (i == 0 || !unicode.IsUpper(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
					if i > 0 {
						flushSub() // 大写前边界 + 缩写连续大写后接小写边界（HTTPServer→http,server）
					}
				}
				cur = append(cur, r)
			}
			flushSub()
			if len(subs) > 1 {
				tokens = append(tokens, subs...)
			}
			current.Reset()
		}
	}

	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r): // 中文汉字
			flushWord()
			chineseRunes = append(chineseRunes, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_': // 英文/代码/数字
			flushChinese()
			current.WriteRune(r) // 保留原始大小写（驼峰边界在 flushWord 里拆分，输出统一 ToLower）
		default: // 分隔符
			flushChinese()
			flushWord()
		}
	}
	flushChinese()
	flushWord()
	return tokens
}

func toSet(tokens []string) map[string]bool {
	s := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		s[t] = true
	}
	return s
}

func (s *RelevanceScorer) Threshold() types.Score {
	return s.threshold
}

func (s *RelevanceScorer) SetThreshold(t types.Score) {
	s.threshold = t
}
