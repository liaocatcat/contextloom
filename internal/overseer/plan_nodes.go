package overseer

import (
	"fmt"
	"strings"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// PlanNodes partitions a list of tasks into plan nodes based on task complexity.
//
// Algorithm:
//   - Simple tasks (complexity=1): merge up to 3 tasks into one node
//   - Medium tasks (complexity=2): merge up to 2 tasks into one node
//   - Complex tasks (complexity>=3): each task gets its own node
//
// Tasks are processed in the given order. Consecutive tasks of the same
// complexity level are batched together. When the batch limit is reached
// or the complexity changes, the current batch is emitted as a node.
//
// Tasks not found in the complexity map default to complexity 1 (simple).
func PlanNodes(tasks []string, complexity map[string]int) []string {
	if len(tasks) == 0 {
		return nil
	}

	var nodes []string
	var currentBatch []string
	currentCpx := -1

	for _, task := range tasks {
		cpx, ok := complexity[task]
		if !ok {
			cpx = 1 // default to simple
		}

		batchSize := batchSizeFor(cpx)

		// Emit current batch when complexity changes or batch is full
		if currentCpx != cpx || len(currentBatch) >= batchSize {
			if len(currentBatch) > 0 {
				nodes = append(nodes, strings.Join(currentBatch, "+"))
			}
			currentBatch = nil
			currentCpx = cpx
		}

		currentBatch = append(currentBatch, task)
	}

	// Flush remaining batch
	if len(currentBatch) > 0 {
		nodes = append(nodes, strings.Join(currentBatch, "+"))
	}

	return nodes
}

// batchSizeFor returns the maximum number of tasks that can be merged into
// a single node for a given complexity level.
func batchSizeFor(complexity int) int {
	switch {
	case complexity <= 1:
		return 3 // simple: up to 3 tasks per node
	case complexity == 2:
		return 2 // medium: up to 2 tasks per node
	default:
		return 1 // complex: one task per node
	}
}

// PlanNode 计划节点（多agent实现建议.md §3.2）
type PlanNode struct {
	ID           string                 `json:"id"`
	TaskIDs      []string               `json:"task_ids"`
	Complexity   int                    `json:"complexity"`
	Directive    string                 `json:"directive"`
	Prompt       string                 `json:"prompt"`
	Manifest     *types.ContextManifest `json:"manifest,omitempty"`
	Dependencies []string               `json:"dependencies,omitempty"`
	Priority     int                    `json:"priority"`
	Status       string                 `json:"status"`
	Score        types.Score            `json:"score"`
	AgentID      string                 `json:"agent_id"`
}

// PlanNodesStructured 返回结构化的 PlanNode 列表
func PlanNodesStructured(tasks []TaskSpec, complexity int) []PlanNode {
	if len(tasks) == 0 {
		return nil
	}
	var nodes []PlanNode
	for i, t := range tasks {
		node := PlanNode{
			ID:         fmt.Sprintf("node-%d", i),
			TaskIDs:    []string{t.Task},
			Complexity: complexity,
			Directive:  t.Directive,
			Prompt:     t.Task,
			Priority:   1,
			Status:     "pending",
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// TaskSpec 任务规格（用于 PlanNodesStructured）
type TaskSpec struct {
	Directive  string
	Task       string
	Complexity int
}
