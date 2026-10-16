package epic

import (
	"fmt"
	"strings"
	"testing"
)

func itoa(i int) string { return fmt.Sprintf("%d", i) }

func validPlan() Plan {
	return Plan{
		Requirements: []Requirement{
			{ID: "R1", Text: "workspace model"},
			{ID: "R2", Text: "membership API"},
			{ID: "R3", Text: "invite flow"},
		},
		Nodes: []Node{
			{Key: "T1", Title: "schema", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R1"}, BranchTarget: "epic/e1"},
			{Key: "T2", Title: "api", AcceptanceCommands: []string{"go test ./api/..."},
				RequirementIDs: []string{"R2"}, BranchTarget: "epic/e1"},
			{Key: "T3", Title: "invites", AcceptanceCommands: []string{"go test ./invite/..."},
				RequirementIDs: []string{"R3"}, BranchTarget: "epic/e1"},
		},
		Edges: []Edge{
			{Predecessor: "T1", Successor: "T2", Reason: "api uses the model"},
			{Predecessor: "T1", Successor: "T3", Reason: "invites reference workspaces"},
		},
	}
}

func TestValidateAcceptsAValidPlan(t *testing.T) {
	if errs := Validate(validPlan()); len(errs) != 0 {
		t.Errorf("Validate rejected a valid plan: %v", errs)
	}
}

func TestValidateRejectsCycles(t *testing.T) {
	p := validPlan()
	p.Edges = append(p.Edges, Edge{Predecessor: "T3", Successor: "T1", Reason: "loop"})
	errs := Validate(p)
	if !hasError(errs, "cycle") {
		t.Errorf("expected a cycle error, got %v", errs)
	}
}

func TestValidateRejectsSelfEdgeAndUnknownNodes(t *testing.T) {
	p := validPlan()
	p.Edges = append(p.Edges,
		Edge{Predecessor: "T1", Successor: "T1", Reason: "self"},
		Edge{Predecessor: "T1", Successor: "T9", Reason: "ghost"},
	)
	errs := Validate(p)
	if !hasError(errs, "depends on itself") {
		t.Errorf("expected a self-edge error, got %v", errs)
	}
	if !hasError(errs, "not a node") {
		t.Errorf("expected an unknown-node error, got %v", errs)
	}
}

func TestValidateRejectsDuplicateKeysAndBadIDs(t *testing.T) {
	p := validPlan()
	p.Nodes = append(p.Nodes, p.Nodes[0])
	p.Requirements = append(p.Requirements, Requirement{ID: "1bad", Text: "x"})
	errs := Validate(p)
	if !hasError(errs, "duplicate node key") {
		t.Errorf("expected a duplicate-key error, got %v", errs)
	}
	if !hasError(errs, "well-formed") {
		t.Errorf("expected a malformed-ID error, got %v", errs)
	}
}

// Coverage runs both ways: no requirement without a node, no node without
// a requirement.
func TestValidateRejectsCoverageGaps(t *testing.T) {
	p := validPlan()
	p.Nodes[2].RequirementIDs = []string{"R9"} // unknown + leaves R3 uncovered
	p.Nodes[0].RequirementIDs = nil            // orphan node
	errs := Validate(p)
	for _, want := range []string{"unknown requirement", "covered by no node", "no ticket without a reason"} {
		if !hasError(errs, want) {
			t.Errorf("expected %q error, got %v", want, errs)
		}
	}
}

func TestValidateRejectsMissingAcceptanceAndBranchTarget(t *testing.T) {
	p := validPlan()
	p.Nodes[0].AcceptanceCommands = nil
	p.Nodes[1].BranchTarget = ""
	errs := Validate(p)
	if !hasError(errs, "no acceptance commands") {
		t.Errorf("expected an acceptance error, got %v", errs)
	}
	if !hasError(errs, "no branch target") {
		t.Errorf("expected a branch-target error, got %v", errs)
	}
}

func TestValidateRejectsOversizedPlans(t *testing.T) {
	p := Plan{Requirements: []Requirement{{ID: "R1", Text: "big"}}}
	for i := 0; i < MaxNodes+1; i++ {
		key := "T" + itoa(i)
		p.Nodes = append(p.Nodes, Node{Key: key, Title: "n", AcceptanceCommands: []string{"true"},
			RequirementIDs: []string{"R1"}, BranchTarget: "epic/e1"})
	}
	if !hasError(Validate(p), "milestone waves") {
		t.Error("expected an oversized-plan error")
	}
}

// The hash is the approval anchor: identical plans hash identically, and
// any edit changes it.
func TestHashChangesOnAnyEdit(t *testing.T) {
	p := validPlan()
	h1, err := Hash(p)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	h2, _ := Hash(validPlan())
	if h1 != h2 {
		t.Error("identical plans hashed differently")
	}
	p.Nodes[0].Title += " "
	h3, _ := Hash(p)
	if h3 == h1 {
		t.Error("an edit left the hash unchanged — approvals would silently carry over")
	}
}

func hasError(errs []error, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return true
		}
	}
	return false
}

// Ticket IDs lower-case the key, so keys equal up to case would collide.
func TestValidateRejectsCaseInsensitiveDuplicateKeys(t *testing.T) {
	p := validPlan()
	p.Nodes[2].Key = "t1"
	p.Edges = p.Edges[:1]
	if errs := Validate(p); !hasError(errs, "duplicate node key") {
		t.Errorf("expected a duplicate key error, got %v", errs)
	}
}
