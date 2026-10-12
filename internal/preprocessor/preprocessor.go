package preprocessor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

type Preprocessor struct {
	mapper      *SemanticMapper
	vocab       *VocabManager
	cacheMu     sync.RWMutex
	promptCache map[string]*StructuredPrompt
}

func New(vocabPath string) *Preprocessor {
	return &Preprocessor{
		mapper:      NewSemanticMapper(),
		vocab:       NewVocabManager(vocabPath),
		promptCache: make(map[string]*StructuredPrompt),
	}
}

// Vocab 返回词汇管理器（供 Runtime 调用提案/确认方法）
func (p *Preprocessor) Vocab() *VocabManager {
	return p.vocab
}

func (p *Preprocessor) Process(userInput string) (*StructuredPrompt, error) {
	// 动态提示词缓存（§3.5）：相同输入哈希缓存
	cacheKey := hashInput(userInput)
	p.cacheMu.RLock()
	if cached, ok := p.promptCache[cacheKey]; ok {
		p.cacheMu.RUnlock()
		return cached, nil
	}
	p.cacheMu.RUnlock()

	intent, entities := p.mapper.Parse(userInput)
	mapped := p.vocab.Map(entities)

	// 组合与冲突消解（§3.2 第3级）
	compressed := intent
	for _, e := range mapped {
		compressed += "_" + e
	}
	if len(compressed) > 12 && len(intent) > 4 {
		shortIntent := intent[:4]
		compressed = shortIntent
		for _, e := range mapped {
			compressed += "_" + e
		}
	}

	result := &StructuredPrompt{
		Directive:  string(types.DirectiveCodeAgent),
		Task:       intent,
		Scope:      mapped,
		Compressed: compressed,
	}

	// 存入缓存（限制大小，防止内存泄漏）
	p.cacheMu.Lock()
	if len(p.promptCache) >= 1000 {
		// 清空旧缓存
		p.promptCache = make(map[string]*StructuredPrompt)
	}
	p.promptCache[cacheKey] = result
	p.cacheMu.Unlock()

	return result, nil
}

type StructuredPrompt struct {
	Directive   string         `json:"directive"`
	Task        string         `json:"task"`
	Scope       []string       `json:"scope,omitempty"`
	Constraints map[string]any `json:"constraints,omitempty"`
	ContextKeys []string       `json:"context_keys,omitempty"`
	Compressed  string         `json:"compressed,omitempty"` // §3.2 组合结果 {INTENT}_{ENTITY}...
}

func (sp *StructuredPrompt) JSON() ([]byte, error) {
	return json.Marshal(sp)
}

func hashInput(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}
