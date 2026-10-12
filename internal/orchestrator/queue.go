package orchestrator

// readyQueue is the live ready-set of the DAG scheduler (§4.1.2). It tracks
// the dynamic indegree of every node and yields, wave by wave, the nodes
// whose dependencies are all finished and which have not been dispatched.
type readyQueue struct {
	remaining map[string]int  // dynamic indegree (-1 = taken in current wave)
	done      map[string]bool // terminal state (completed or failed)
	order     []string        // deterministic declaration order
}

func newReadyQueue(g *Graph) *readyQueue {
	q := &readyQueue{
		remaining: make(map[string]int, len(g.nodes)),
		done:      make(map[string]bool, len(g.nodes)),
		order:     append([]string(nil), g.order...),
	}
	for id, deg := range g.indegree {
		q.remaining[id] = deg
	}
	return q
}

// take returns up to limit ready nodes (indegree 0, not done, not already
// taken in this wave) in declaration order. limit <= 0 means unlimited.
func (q *readyQueue) take(limit int) []string {
	var batch []string
	for _, id := range q.order {
		if limit > 0 && len(batch) == limit {
			break
		}
		if q.done[id] || q.remaining[id] != 0 {
			continue
		}
		q.remaining[id] = -1 // taken in this wave
		batch = append(batch, id)
	}
	return batch
}

// complete marks a node finished and releases its dependents.
func (q *readyQueue) complete(g *Graph, id string) {
	if q.done[id] {
		return
	}
	q.done[id] = true
	for _, next := range g.dependents[id] {
		if q.remaining[next] > 0 {
			q.remaining[next]--
		}
	}
}

// fail marks a node permanently failed without releasing its dependents:
// nodes blocked behind a failed node must never become ready.
func (q *readyQueue) fail(id string) {
	q.done[id] = true
}

// allDone reports whether every node reached a terminal state (done/failed).
func (q *readyQueue) allDone() bool {
	for _, id := range q.order {
		if !q.done[id] {
			return false
		}
	}
	return true
}
