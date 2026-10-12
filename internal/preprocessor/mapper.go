package preprocessor

import (
	"strings"
	"unicode"
)

type SemanticMapper struct {
	intents  map[string]string
	entities map[string]string
}

func NewSemanticMapper() *SemanticMapper {
	return &SemanticMapper{
		intents: map[string]string{
			"fix": "BUG_FIX", "bug": "BUG_FIX", "error": "BUG_FIX", "crash": "BUG_FIX",
			"refactor": "REFACTOR", "refact": "REFACTOR", "restructure": "REFACTOR",
			"add": "FEATURE", "new": "FEATURE", "implement": "FEATURE", "create": "FEATURE",
			"explain": "EXPLAIN", "describe": "EXPLAIN", "what": "EXPLAIN", "how": "EXPLAIN",
			"test": "TEST", "spec": "TEST", "verify": "TEST", "check": "TEST",
			"optimize": "OPTIMIZE", "perf": "OPTIMIZE", "faster": "OPTIMIZE", "slow": "OPTIMIZE",
			"rewrite": "REWRITE", "migrate": "MIGRATE", "port": "MIGRATE",
			"review": "REVIEW", "audit": "REVIEW",
			"doc": "DOC", "document": "DOC", "readme": "DOC",
		},
		entities: map[string]string{
			"function": "FUNC", "func": "FUNC", "method": "FUNC",
			"class": "CLASS", "struct": "STRUCT", "interface": "IFACE",
			"module": "MOD", "package": "PKG", "file": "FILE",
			"test": "TEST", "config": "CFG", "api": "API",
			"null": "NULL", "pointer": "PTR", "exception": "EXC",
			"type": "TYPE", "string": "STR", "int": "INT",
		},
	}
}

func (m *SemanticMapper) Parse(input string) (intent string, entities []string) {
	lower := strings.ToLower(input)
	words := splitWords(lower)

	intentCount := make(map[string]int)
	firstIndex := make(map[string]int)
	idx := 0
	for _, word := range words {
		if intentClass, ok := m.intents[word]; ok {
			intentCount[intentClass]++
			if _, exists := firstIndex[intentClass]; !exists {
				firstIndex[intentClass] = idx
			}
		}
		idx++
	}

	intent = "TASK"
	bestCount := 0
	for ic, count := range intentCount {
		if count > bestCount || (count == bestCount && firstIndex[ic] < firstIndex[intent]) {
			bestCount = count
			intent = ic
		}
	}

	seen := make(map[string]bool)
	entityWords := make([]string, 0)
	for _, word := range words {
		if _, ok := m.intents[word]; ok {
			continue
		}
		if mapped, ok := m.entities[word]; ok {
			if !seen[mapped] {
				entityWords = append(entityWords, mapped)
				seen[mapped] = true
			}
		} else if len(word) > 2 && !seen[word] {
			entityWords = append(entityWords, word)
			seen[word] = true
		}
	}

	// 组合与冲突消解（§3.2 第3级）—— 不修改 intent，只在需要时缩短
	// 最终结果为 {INTENT}_{ENTITY1}_{ENTITY2}...
	// 若组合长度超过 12 字符则缩短意图词，但只影响组合结果，不影响 intent 返回值

	return intent, entityWords
}

func splitWords(s string) []string {
	var words []string
	current := strings.Builder{}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-' || r == '/' {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
			}
		}
	}
	if current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}
