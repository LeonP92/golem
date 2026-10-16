package epic

// Ready returns the nodes that are not integrated and whose hard
// predecessors all are. "Integrated" means merged into the branch target;
// a closed or ready-for-review ticket does not count.
func Ready(p Plan, integrated map[string]bool) map[string]bool {
	ready := map[string]bool{}
	for _, n := range p.Nodes {
		ready[n.Key] = !integrated[n.Key]
	}
	for _, e := range p.Edges {
		if !integrated[e.Predecessor] {
			ready[e.Successor] = false
		}
	}
	return ready
}
