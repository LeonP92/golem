package epic

import "sort"

// Integrated is the one fact readiness cares about: a node whose code has
// been merged into its branch target with passing evidence. A node that is
// closed, pushed, or even ready-for-review does not count — the design's
// rule is that a dependency means code is integrated, not that work ended.
type State struct {
	Integrated map[string]bool // node key -> integrated receipt exists
}

// Ready returns the keys of every node whose hard predecessors are all
// integrated and which is not integrated itself, in scheduling priority
// order: critical-path depth first (a long chain started early finishes
// early), then plan order as the stable tie-break. Readiness here is the
// graph property only; the orchestrator additionally requires a running
// epic, approved plan membership, no unresolved human decision, and a free
// concurrency slot before anything is dispatched.
func Ready(p Plan, st State) []string {
	depth := criticalPathDepth(p)
	var ready []string
	for _, n := range p.Nodes {
		if st.Integrated[n.Key] {
			continue
		}
		blocked := false
		for _, e := range p.Edges {
			if e.Successor == n.Key && !st.Integrated[e.Predecessor] {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, n.Key)
		}
	}
	order := map[string]int{}
	for i, n := range p.Nodes {
		order[n.Key] = i
	}
	sort.SliceStable(ready, func(i, j int) bool {
		if depth[ready[i]] != depth[ready[j]] {
			return depth[ready[i]] > depth[ready[j]]
		}
		return order[ready[i]] < order[ready[j]]
	})
	return ready
}

// criticalPathDepth is the longest chain of hard successors hanging off
// each node. Computed on the validated DAG; a cyclic plan yields depth 0
// everywhere, which is fine — Validate rejects cycles before scheduling.
func criticalPathDepth(p Plan) map[string]int {
	succ := map[string][]string{}
	for _, e := range p.Edges {
		succ[e.Predecessor] = append(succ[e.Predecessor], e.Successor)
	}
	depth := map[string]int{}
	var compute func(key string) int
	compute = func(key string) int {
		if d, ok := depth[key]; ok {
			return d
		}
		best := 0
		for _, next := range succ[key] {
			if d := compute(next) + 1; d > best {
				best = d
			}
		}
		depth[key] = best
		return best
	}
	for _, n := range p.Nodes {
		compute(n.Key)
	}
	return depth
}
