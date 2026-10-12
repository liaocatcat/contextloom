package overseer

import (
	"context"
	"sort"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

type Overseer struct {
	mu            sync.RWMutex
	goal          string
	constraints   map[string]any
	passThreshold types.Score
	rules         *ScoringRules
	checkpoints   []types.OverseerCheckpoint
	// §9.4 复盘节点树：监工维护各子任务节点的状态流转
	nodes map[string]*NodeState
}

// NodeState 是复盘节点的运行状态（§9.4）
type NodeState struct {
	ID       string      // 节点 ID（= 子 Agent ID）
	Task     string      // 子任务描述
	Status   string      // pending → running → passed/failed
	Score    types.Score // 监工评分
	Attempts int         // 尝试次数
}

func New(goal string, constraints map[string]any, passThreshold types.Score, rules *ScoringRules) *Overseer {
	if rules == nil {
		rules = DefaultScoringRules()
	}
	return &Overseer{
		goal:          goal,
		constraints:   constraints,
		passThreshold: passThreshold,
		rules:         rules,
		nodes:         make(map[string]*NodeState),
	}
}

func (o *Overseer) Evaluate(ctx context.Context, agentID string, report types.OverseerReport) bool {
	score := o.rules.Calculate(report)
	report.Score = score
	passed := score >= o.passThreshold

	reason := report.Summary

	// Anti-cheat: verify execution integrity when claimed and actual results are provided.
	// If VerifyExecution detects cheating (similarity < 0.8), the evaluation fails
	// and the agent's previous checkpoints are purged to prevent contamination.
	if report.ClaimedResult != nil && report.ActualResult != nil {
		_, trusted := VerifyExecution(report.ClaimedResult, report.ActualResult)
		if !trusted {
			passed = false
			reason = "CHEATING_DETECTED: " + report.Summary
			// Purge this agent's checkpoints to prevent process contamination
			o.purgeAgentCheckpoints(agentID)
		}
	}

	// Constraints act as gating factors: even if the weighted score passes,
	// failing a constraint overrides the result.
	if !o.validateConstraints(report) {
		passed = false
		if reason == report.Summary {
			reason = "约束验证失败: " + report.Summary
		}
	}

	o.mu.Lock()
	o.checkpoints = append(o.checkpoints, types.OverseerCheckpoint{
		NodeName:     report.NodeName,
		ChildAgentID: agentID,
		Approved:     passed,
		Reason:       reason,
	})
	o.mu.Unlock()

	return passed
}

// purgeAgentCheckpoints removes all checkpoints for a specific agent.
// Called when cheating is detected to prevent process contamination.
// Must be called while o.mu is NOT held (Evaluate acquires the lock after).
func (o *Overseer) purgeAgentCheckpoints(agentID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	filtered := make([]types.OverseerCheckpoint, 0, len(o.checkpoints))
	for _, cp := range o.checkpoints {
		if cp.ChildAgentID != agentID {
			filtered = append(filtered, cp)
		}
	}
	o.checkpoints = filtered
}

func (o *Overseer) validateConstraints(report types.OverseerReport) bool {
	for key, expected := range o.constraints {
		switch key {
		case "min_test_coverage":
			if expectedVal, ok := expected.(float64); ok && report.TestCoverage < expectedVal {
				return false
			}
		case "min_code_quality":
			if expectedVal, ok := expected.(float64); ok && report.CodeQuality < expectedVal {
				return false
			}
		}
	}
	return true
}

// CalculateScore 计算报告的加权总分（不做通过判定），供外部展示评估分数
func (o *Overseer) CalculateScore(report types.OverseerReport) types.Score {
	return o.rules.Calculate(report)
}

func (o *Overseer) Goal() string {
	return o.goal
}

func (o *Overseer) Constraints() map[string]any {
	return o.constraints
}

func (o *Overseer) Checkpoints() []types.OverseerCheckpoint {
	o.mu.RLock()
	defer o.mu.RUnlock()
	cp := make([]types.OverseerCheckpoint, len(o.checkpoints))
	copy(cp, o.checkpoints)
	return cp
}

// RegisterNode 注册复盘节点（§9.4），子 Agent 启动时调用
func (o *Overseer) RegisterNode(agentID, task string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.nodes == nil {
		o.nodes = make(map[string]*NodeState)
	}
	if existing, ok := o.nodes[agentID]; ok {
		existing.Attempts++
		existing.Status = "running"
		return
	}
	o.nodes[agentID] = &NodeState{
		ID:     agentID,
		Task:   task,
		Status: "running",
	}
}

// UpdateNodeStatus 更新节点状态（§9.4），监工评估后调用
func (o *Overseer) UpdateNodeStatus(agentID string, passed bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if node, ok := o.nodes[agentID]; ok {
		if passed {
			node.Status = "passed"
		} else {
			node.Status = "failed"
		}
	}
}

// NodeStates 返回所有节点状态的快照（供 AI 参考编排决策）
func (o *Overseer) NodeStates() []NodeState {
	o.mu.RLock()
	defer o.mu.RUnlock()
	result := make([]NodeState, 0, len(o.nodes))
	for _, ns := range o.nodes {
		result = append(result, *ns)
	}
	// 按 ID 排序，保证返回顺序确定（map 遍历无序）
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
