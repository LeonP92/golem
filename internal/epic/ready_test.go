package epic

import (
	"reflect"
	"testing"
)

// Only T1 is ready at the start: everything else waits on its integration.
// Ready-for-review or closed would not unblock them — only an integrated
// predecessor counts.
func TestReadyRequiresIntegration(t *testing.T) {
	p := validPlan()
	got := Ready(p, State{Integrated: map[string]bool{}})
	if !reflect.DeepEqual(got, []string{"T1"}) {
		t.Errorf("Ready at start = %v, want [T1]", got)
	}
}

// Integrating T1 releases BOTH successors at once — the scheduler releases
// every ready node, not one arbitrary layer at a time.
func TestReadyReleasesAllReadyNodes(t *testing.T) {
	p := validPlan()
	got := Ready(p, State{Integrated: map[string]bool{"T1": true}})
	if !reflect.DeepEqual(got, []string{"T2", "T3"}) {
		t.Errorf("Ready after T1 = %v, want [T2 T3]", got)
	}
}

// Priority is critical-path depth first: a node at the head of a long
// chain goes before an independent node even when the independent one
// appears earlier in plan order.
func TestReadyPrioritisesCriticalPath(t *testing.T) {
	p := Plan{
		Requirements: []Requirement{{ID: "R1", Text: "x"}},
		Nodes: []Node{
			{Key: "IND", Title: "independent", AcceptanceCommands: []string{"true"}, RequirementIDs: []string{"R1"}, BranchTarget: "epic/e1"},
			{Key: "A", Title: "chain head", AcceptanceCommands: []string{"true"}, RequirementIDs: []string{"R1"}, BranchTarget: "epic/e1"},
			{Key: "B", Title: "chain mid", AcceptanceCommands: []string{"true"}, RequirementIDs: []string{"R1"}, BranchTarget: "epic/e1"},
			{Key: "C", Title: "chain tail", AcceptanceCommands: []string{"true"}, RequirementIDs: []string{"R1"}, BranchTarget: "epic/e1"},
		},
		Edges: []Edge{
			{Predecessor: "A", Successor: "B", Reason: "chain"},
			{Predecessor: "B", Successor: "C", Reason: "chain"},
		},
	}
	got := Ready(p, State{Integrated: map[string]bool{}})
	if !reflect.DeepEqual(got, []string{"A", "IND"}) {
		t.Errorf("Ready = %v, want [A IND] (critical path first)", got)
	}
}

// An integrated node never re-enters the ready set.
func TestReadySkipsIntegratedNodes(t *testing.T) {
	p := validPlan()
	got := Ready(p, State{Integrated: map[string]bool{"T1": true, "T2": true}})
	if !reflect.DeepEqual(got, []string{"T3"}) {
		t.Errorf("Ready = %v, want [T3]", got)
	}
}
