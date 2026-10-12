package executor

import (
	"fmt"
	"strings"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// AgentResult 收集和合并多 Agent 结果（多agent实现建议.md §3.3）
type AgentResult struct {
	AgentID      string      `json:"agent_id"`
	NodeID       string      `json:"node_id"`
	Status       string      `json:"status"`
	Output       string      `json:"output"`
	FilesChanged []string    `json:"files_changed,omitempty"`
	TestsPassed  bool        `json:"tests_passed"`
	Score        types.Score `json:"score"`
	TokensUsed   int         `json:"tokens_used"`
	Error        string      `json:"error,omitempty"`
}

// ResultCollector 收集和合并多 Agent 结果
type ResultCollector struct {
	mu      sync.Mutex
	results map[string]*AgentResult
}

func NewResultCollector() *ResultCollector {
	return &ResultCollector{results: make(map[string]*AgentResult)}
}

func (rc *ResultCollector) Add(r *AgentResult) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.results[r.AgentID] = r
}

func (rc *ResultCollector) Get(agentID string) *AgentResult {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.results[agentID]
}

func (rc *ResultCollector) Merge() string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	var parts []string
	for _, r := range rc.results {
		header := fmt.Sprintf("## %s (评分: %.0f)", r.NodeID, r.Score)
		parts = append(parts, header+"\n"+r.Output)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// DetectConflicts 检测多个 Agent 是否修改了同一文件
func (rc *ResultCollector) DetectConflicts() []string {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	fileOwners := make(map[string][]string)
	for agentID, r := range rc.results {
		for _, f := range r.FilesChanged {
			fileOwners[f] = append(fileOwners[f], agentID)
		}
	}
	var conflicts []string
	for file, agents := range fileOwners {
		if len(agents) > 1 {
			conflicts = append(conflicts,
				fmt.Sprintf("冲突: %s 被 %v 同时修改", file, agents))
		}
	}
	return conflicts
}
