package epics

import (
	"testing"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

func runningEpic(t *testing.T) (*db.Epic, *laneFixture) {
	t.Helper()
	f := newLaneFixture(t, true)
	var e db.Epic
	f.gdb.First(&e, "id = ?", f.epic.ID)
	if e.State != "running" {
		t.Fatalf("state = %q", e.State)
	}
	return &e, f
}

// Pause and resume refuse the states where they make no sense — a paused
// run cannot pause again, a running one cannot resume, and
// a paused run is the only one that resumes.
func TestPauseResumeStateRules(t *testing.T) {
	e, f := runningEpic(t)
	if err := Resume(f.gdb, e.ID); err == nil {
		t.Fatal("resume of a running epic must fail")
	}
	if err := Pause(f.gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := Pause(f.gdb, e.ID); err == nil {
		t.Fatal("double pause must fail")
	}
}

// A failed node — one that never integrates — blocks only its
// descendants: readiness derives from receipts through the plan's edges,
// so siblings on other branches stay ready.
func TestFailedNodeBlocksOnlyDescendants(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{RepoRemote: "example.com/x/y", Title: "t", SpecMarkdown: "s"})
	if err != nil {
		t.Fatal(err)
	}
	// A -> B -> D on one branch, A -> C on the other. B fails.
	plan := epic.Plan{
		Requirements: []epic.Requirement{{ID: "R1", Text: "a"}, {ID: "R2", Text: "b"}, {ID: "R3", Text: "c"}, {ID: "R4", Text: "d"}},
		Nodes: []epic.Node{
			{Key: "A", Title: "root", AcceptanceCommands: []string{"t"}, RequirementIDs: []string{"R1"}},
			{Key: "B", Title: "fails", AcceptanceCommands: []string{"t"}, RequirementIDs: []string{"R2"}},
			{Key: "C", Title: "fine", AcceptanceCommands: []string{"t"}, RequirementIDs: []string{"R3"}},
			{Key: "D", Title: "behind b", AcceptanceCommands: []string{"t"}, RequirementIDs: []string{"R4"}},
		},
		Edges: []epic.Edge{
			{Predecessor: "A", Successor: "B", Reason: "b needs a"},
			{Predecessor: "B", Successor: "D", Reason: "d needs b"},
			{Predecessor: "A", Successor: "C", Reason: "c needs a"},
		},
	}
	rev, err := AddRevision(gdb, e.ID, plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	// A integrates; B fails and never will. C must still unblock.
	if err := RecordIntegration(gdb, e.ID, "A", "sha-a"); err != nil {
		t.Fatal(err)
	}
	ready := map[string]bool{}
	var tickets []db.Ticket
	gdb.Where("epic_id = ?", e.ID).Find(&tickets)
	for _, tk := range tickets {
		ready[tk.EpicNodeKey] = tk.EpicReady
	}
	if ready["D"] {
		t.Fatal("descendant of the failed node offered as ready")
	}
	if !ready["B"] || !ready["C"] {
		t.Fatalf("unrelated branches starved: ready=%v", ready)
	}
}

// A merge observed while the run is paused is still a fact: the receipt
// lands, successors become ready on resume, and the last receipt finishes
// the run even if it arrives during a pause.
func TestReceiptWhilePausedIsKept(t *testing.T) {
	e, f := runningEpic(t)
	if err := Pause(f.gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := RecordIntegration(f.gdb, e.ID, "T1", "m1"); err != nil {
		t.Fatalf("receipt while paused: %v", err)
	}
	if err := Resume(f.gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	var t2 db.Ticket
	f.gdb.First(&t2, "epic_id = ? AND epic_node_key = 'T2'", e.ID)
	if !t2.EpicReady {
		t.Fatal("T2 not ready after its predecessor's receipt")
	}
	if err := Pause(f.gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := RecordIntegration(f.gdb, e.ID, "T2", "m2"); err != nil {
		t.Fatalf("last receipt while paused: %v", err)
	}
	var got db.Epic
	f.gdb.First(&got, "id = ?", e.ID)
	if got.State != "awaiting-final-review" {
		t.Fatalf("state = %q, want awaiting-final-review", got.State)
	}
}
