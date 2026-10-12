package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Status of a team run.
type Status string

const (
	// StatusCompleted means every node finished (after retries) successfully.
	StatusCompleted Status = "completed"
	// StatusFailed means at least one node exhausted its retries; nodes
	// blocked behind failed nodes are never dispatched.
	StatusFailed Status = "failed"
)

// NodeExecutor executes a single team node and returns its output.
// Implementations inject the real model/toolchain/employee runtime; the
// scheduling kernel stays fully decoupled.
type NodeExecutor func(ctx context.Context, node NodeSpec) (string, error)

// TeamResult is the outcome of a team run: the deliverable collected from
// the output.from node, per-node outputs and the overall status.
type TeamResult struct {
	Output      string
	NodeOutputs map[string]string
	Status      Status
}

// Run schedules plan topologically (§4.1.2). Ready nodes run in parallel
// waves of at most policy.max_fanout goroutines (<=0 = unlimited); each node
// is retried policy.retry times on failure. The deliverable is collected
// from output.from. When a node exhausts its retries, Run stops dispatching
// nodes blocked behind it and returns an error naming every failed node's
// id and role.
func Run(ctx context.Context, plan TeamPlan, executor NodeExecutor) (*TeamResult, error) {
	if executor == nil {
		return nil, fmt.Errorf("orchestrator: team %q: nil node executor", plan.Name)
	}
	g, err := BuildGraph(plan)
	if err != nil {
		return nil, err
	}
	res := &TeamResult{
		NodeOutputs: make(map[string]string, len(g.nodes)),
		Status:      StatusCompleted,
	}
	q := newReadyQueue(g)
	limit := plan.Policy.MaxFanout
	retries := plan.Policy.Retry
	if retries < 0 {
		retries = 0
	}

	var failedErrs []string
	for {
		if q.allDone() {
			break
		}
		if err := ctx.Err(); err != nil {
			res.Status = StatusFailed
			return res, fmt.Errorf("orchestrator: team %q cancelled: %w", plan.Name, err)
		}
		batch := q.take(limit)
		if len(batch) == 0 {
			// No node is ready and not all are done: the remaining nodes
			// are blocked behind failed ones. Stop to avoid spinning.
			break
		}
		for _, r := range runWave(ctx, g, batch, executor, retries) {
			if r.err != nil {
				failedErrs = append(failedErrs, r.err.Error())
				q.fail(r.id)
			} else {
				res.NodeOutputs[r.id] = r.output
				q.complete(g, r.id)
			}
		}
	}

	if len(failedErrs) > 0 {
		res.Status = StatusFailed
		if out, ok := res.NodeOutputs[plan.Output.From]; ok {
			res.Output = out
		}
		return res, fmt.Errorf("orchestrator: team %q: %d node(s) failed:\n%s",
			plan.Name, len(failedErrs), strings.Join(failedErrs, "\n"))
	}
	if from := plan.Output.From; from != "" {
		res.Output = res.NodeOutputs[from]
	}
	return res, nil
}

type waveResult struct {
	id     string
	output string
	err    error
}

// runWave dispatches one batch of ready nodes concurrently and waits for all
// of them; each node gets its full retry budget inside runNodeWithRetry.
func runWave(ctx context.Context, g *Graph, batch []string, executor NodeExecutor, retries int) []waveResult {
	results := make([]waveResult, len(batch))
	var wg sync.WaitGroup
	for i, id := range batch {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			node := g.nodes[id]
			out, err := runNodeWithRetry(ctx, node, executor, retries)
			results[i] = waveResult{id: id, output: out, err: err}
		}(i, id)
	}
	wg.Wait()
	return results
}

// runNodeWithRetry executes one node, retrying failures up to the plan's
// retry budget (attempts = retry+1). A cancelled context aborts retries.
// The returned error names the node id and role for readability.
func runNodeWithRetry(ctx context.Context, node NodeSpec, executor NodeExecutor, retries int) (string, error) {
	attempts := retries + 1
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		out, err := executor(ctx, node)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}
	return "", fmt.Errorf("node %q (role %q) failed after %d attempt(s): %v", node.ID, node.Role, attempts, lastErr)
}
