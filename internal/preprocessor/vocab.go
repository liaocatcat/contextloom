package preprocessor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// VocabProposal 词汇映射提案（模型发现新术语时提出，待用户确认后加入词表）
type VocabProposal struct {
	Term    string `json:"term"`    // 原始术语
	Mapping string `json:"mapping"` // 映射目标（缩写/代号）
	Reason  string `json:"reason"`  // 提案理由
}

type VocabManager struct {
	mu        sync.RWMutex
	path      string
	entries   map[string]string
	proposals map[string]VocabProposal // 待确认提案（key=term）
}

func NewVocabManager(path string) *VocabManager {
	v := &VocabManager{
		path:      path,
		entries:   make(map[string]string),
		proposals: make(map[string]VocabProposal),
	}
	v.initDefaults()
	v.Load()
	return v
}

func (v *VocabManager) initDefaults() {
	v.entries["null_pointer"] = "NPE"
	v.entries["authentication"] = "AUTH"
	v.entries["refactor"] = "REFACTOR"
	v.entries["type_hints"] = "TYPING"
	v.entries["sql_injection"] = "SQLI"
	v.entries["bug_fix"] = "BUG_FIX"
	v.entries["module"] = "MOD"
	v.entries["function"] = "FUNC"
}

// Load 从 .contextloom/vocab.json 加载用户自定义词表，合并到默认词表
func (v *VocabManager) Load() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	p := v.path
	if p == "" {
		p = ".contextloom/vocab.json"
	}
	if dir, err := os.UserHomeDir(); err == nil && len(p) > 0 && p[0] == '~' {
		p = filepath.Join(dir, p[1:])
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var fileVocab map[string]string
	if err := json.Unmarshal(data, &fileVocab); err != nil {
		return err
	}
	for k, val := range fileVocab {
		v.entries[k] = val
	}
	return nil
}

// Save 持久化词表到文件
func (v *VocabManager) Save() error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	p := v.path
	if p == "" {
		p = ".contextloom/vocab.json"
	}
	os.MkdirAll(filepath.Dir(p), 0755)
	data, err := json.MarshalIndent(v.entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0644)
}

func (v *VocabManager) Map(entities []string) []string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	result := make([]string, len(entities))
	for i, entity := range entities {
		if mapped, ok := v.entries[entity]; ok {
			result[i] = mapped
		} else {
			result[i] = entity
		}
	}
	return result
}

func (v *VocabManager) Set(from, to string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.entries[from] = to
}

// Propose 提交词汇映射提案（不立即生效，等用户确认后由 Accept 落地）
func (v *VocabManager) Propose(term, mapping, reason string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.proposals[term] = VocabProposal{Term: term, Mapping: mapping, Reason: reason}
}

// ListPending 返回所有待确认提案
func (v *VocabManager) ListPending() []VocabProposal {
	v.mu.RLock()
	defer v.mu.RUnlock()
	result := make([]VocabProposal, 0, len(v.proposals))
	for _, p := range v.proposals {
		result = append(result, p)
	}
	return result
}

// Accept 确认提案：加入词表 + 持久化 + 移除提案。返回是否找到该提案。
func (v *VocabManager) Accept(term string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	p, ok := v.proposals[term]
	if !ok {
		return false
	}
	v.entries[term] = p.Mapping
	delete(v.proposals, term)
	v.saveLocked()
	return true
}

// Reject 拒绝提案：仅移除。返回是否找到该提案。
func (v *VocabManager) Reject(term string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.proposals[term]
	if !ok {
		return false
	}
	delete(v.proposals, term)
	return true
}

// saveLocked 在已持锁时持久化（Accept 内部调用，避免重复加锁）
func (v *VocabManager) saveLocked() {
	p := v.path
	if p == "" {
		p = ".contextloom/vocab.json"
	}
	os.MkdirAll(filepath.Dir(p), 0755)
	data, _ := json.MarshalIndent(v.entries, "", "  ")
	os.WriteFile(p, data, 0644)
}
