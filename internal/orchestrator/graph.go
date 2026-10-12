package orchestrator

import (
	"fmt"
	"strings"
)

// Graph is the resolved dependency structure of a TeamPlan: a deterministic
// node order, static indegrees and both edge directions.
type Graph struct {
	nodes      map[string]NodeSpec
	order      []string
	indegree   map[string]int
	dependsOn  map[string][]string // node -> nodes it depends on
	dependents map[string][]string // node -> nodes that depend on it
}

// BuildGraph validates the plan and resolves its dependency structure.
// It rejects: empty or duplicate node ids, empty dependency ids,
// dependencies on missing nodes, an output.from referencing a missing node,
// and dependency cycles (reported with a readable cycle path).
func BuildGraph(plan TeamPlan) (*Graph, error) {
	g := &Graph{
		nodes:      make(map[string]NodeSpec, len(plan.Nodes)),
		indegree:   make(map[string]int, len(plan.Nodes)),
		dependsOn:  make(map[string][]string, len(plan.Nodes)),
		dependents: make(map[string][]string, len(plan.Nodes)),
	}
	for _, n := range plan.Nodes {
		if n.ID == "" {
			return nil, fmt.Errorf("orchestrator: team %q: node with empty id", plan.Name)
		}
		if _, dup := g.nodes[n.ID]; dup {
			return nil, fmt.Errorf("orchestrator: team %q: duplicate node id %q", plan.Name, n.ID)
		}
		g.nodes[n.ID] = n
		g.order = append(g.order, n.ID)
	}
	for _, n := range plan.Nodes {
		for _, dep := range n.DependsOn {
			if dep == "" {
				return nil, fmt.Errorf("orchestrator: team %q: node %q has an empty dependency id", plan.Name, n.ID)
			}
			if _, ok := g.nodes[dep]; !ok {
				return nil, fmt.Errorf("orchestrator: team %q: node %q depends on missing node %q", plan.Name, n.ID, dep)
			}
			g.dependsOn[n.ID] = append(g.dependsOn[n.ID], dep)
			g.dependents[dep] = append(g.dependents[dep], n.ID)
			g.indegree[n.ID]++
		}
	}
	if plan.Output.From != "" {
		if _, ok := g.nodes[plan.Output.From]; !ok {
			return nil, fmt.Errorf("orchestrator: team %q: output.from references missing node %q", plan.Name, plan.Output.From)
		}
	}
	if err := g.checkCycles(plan.Name); err != nil {
		return nil, err
	}
	return g, nil
}

// checkCycles runs Kahn's algorithm; any leftover node must participate in
// a cycle, which is then recovered by DFS into a readable path such as
// "a -> c -> b -> a" (each node depends on the next).
func (g *Graph) checkCycles(name string) error {
	indeg := make(map[string]int, len(g.indegree))
	for id, deg := range g.indegree {
		indeg[id] = deg
	}
	queue := make([]string, 0, len(g.order))
	for _, id := range g.order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range g.dependents[id] {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited == len(g.nodes) {
		return nil
	}

	// DFS over the leftover nodes to recover a readable cycle path.
	state := make(map[string]int, len(g.nodes)) // 0=unseen 1=visiting 2=done
	path := make([]string, 0, len(g.nodes))
	var find func(id string) ([]string, bool)
	find = func(id string) ([]string, bool) {
		state[id] = 1
		path = append(path, id)
		for _, dep := range g.dependsOn[id] {
			switch state[dep] {
			case 0:
				if cyc, ok := find(dep); ok {
					return cyc, true
				}
			case 1:
				start := indexOf(path, dep)
				cyc := append(append([]string{}, path[start:]...), dep)
				return cyc, true
			}
		}
		path = path[:len(path)-1]
		state[id] = 2
		return nil, false
	}
	for _, id := range g.order {
		if indeg[id] > 0 && state[id] == 0 {
			if cyc, ok := find(id); ok {
				return fmt.Errorf("orchestrator: team %q: dependency cycle detected: %s (each node depends on the next)",
					name, strings.Join(cyc, " -> "))
			}
		}
	}
	return fmt.Errorf("orchestrator: team %q: dependency cycle detected (no readable path)", name)
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
