package epics

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// diamond: A -> B/C -> D. B and C may run in
// parallel only after A integrates; D waits for both.
func diamondPlan(target string) epic.Plan {
	return epic.Plan{
		Requirements: []epic.Requirement{{ID: "R1", Text: "one"}, {ID: "R2", Text: "two"},
			{ID: "R3", Text: "three"}, {ID: "R4", Text: "four"}},
		Nodes: []epic.Node{
			{Key: "A", Title: "base model", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R1"}, BranchTarget: target},
			{Key: "B", Title: "api", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R2"}, BranchTarget: target},
			{Key: "C", Title: "cli", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R3"}, BranchTarget: target},
			{Key: "D", Title: "docs", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R4"}, BranchTarget: target},
		},
		Edges: []epic.Edge{
			{Predecessor: "A", Successor: "B", Reason: "api uses the model"},
			{Predecessor: "A", Successor: "C", Reason: "cli uses the model"},
			{Predecessor: "B", Successor: "D", Reason: "docs describe the api"},
			{Predecessor: "C", Successor: "D", Reason: "docs describe the cli"},
		},
	}
}

func approvedDiamond(t *testing.T, gdb *gorm.DB) *db.Epic {
	t.Helper()
	e := createEpic(t, gdb)
	if _, err := AddRevision(gdb, e.ID, diamondPlan(e.IntegrationRef), ""); err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	_, rev, err := LoadPlan(gdb, e.ID, 1)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if err := Approve(gdb, e.ID, 1, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	return e
}

func epicChildren(t *testing.T, gdb *gorm.DB, epicID string) []db.Ticket {
	t.Helper()
	var ts []db.Ticket
	if err := gdb.Where("epic_id = ?", epicID).Order("id").Find(&ts).Error; err != nil {
		t.Fatalf("children: %v", err)
	}
	return ts
}

func readyKeys(t *testing.T, gdb *gorm.DB, epicID string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, c := range epicChildren(t, gdb, epicID) {
		out[c.EpicNodeKey] = c.EpicReady
	}
	return out
}

func assertChildPhase(t *testing.T, gdb *gorm.DB, epicID, nodeKey, want string) {
	t.Helper()
	var child db.Ticket
	if err := gdb.Where("epic_id = ? AND epic_node_key = ?", epicID, nodeKey).First(&child).Error; err != nil {
		t.Fatalf("load child %s: %v", nodeKey, err)
	}
	if child.Phase != want {
		t.Errorf("child %s phase = %q, want %q", nodeKey, child.Phase, want)
	}
}

func TestStartMaterializesChildrenOnce(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := approvedDiamond(t, gdb)

	created, err := Start(gdb, e.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created != 4 {
		t.Fatalf("created = %d, want 4", created)
	}
	children := epicChildren(t, gdb, e.ID)
	if len(children) != 4 {
		t.Fatalf("children = %d", len(children))
	}
	for _, c := range children {
		if c.Phase != "unassigned" || c.BaseBranch != e.IntegrationRef {
			t.Errorf("child %s: phase %q base %q", c.EpicNodeKey, c.Phase, c.BaseBranch)
		}
		if c.EpicPlanRevision != 1 || c.EpicID == nil || *c.EpicID != e.ID {
			t.Errorf("child %s: epic fields %v %d", c.EpicNodeKey, c.EpicID, c.EpicPlanRevision)
		}
		if c.Description == "" {
			t.Errorf("child %s: empty description", c.EpicNodeKey)
		}
	}
	// Only source nodes are ready at start; the epic is running.
	ready := readyKeys(t, gdb, e.ID)
	if !ready["A"] || ready["B"] || ready["C"] || ready["D"] {
		t.Fatalf("readiness at start = %v", ready)
	}
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.State != "running" {
		t.Fatalf("state = %q", fresh.State)
	}
	// Restart/repeated start never duplicates children.
	again, err := Start(gdb, e.ID)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if again != 0 || len(epicChildren(t, gdb, e.ID)) != 4 {
		t.Fatalf("second start created %d, total %d", again, len(epicChildren(t, gdb, e.ID)))
	}
}

func TestStartRequiresApproval(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	if _, err := AddRevision(gdb, e.ID, diamondPlan(e.IntegrationRef), ""); err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	if _, err := Start(gdb, e.ID); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("Start without approval = %v", err)
	}
	var n int64
	gdb.Model(&db.Ticket{}).Where("epic_id = ?", e.ID).Count(&n)
	if n != 0 {
		t.Fatalf("failed start left %d children", n)
	}
}

// B and C become ready together only after A
// integrates; D waits for both.
func TestReadinessFollowsIntegrationReceipts(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := approvedDiamond(t, gdb)
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := RecordIntegration(gdb, e.ID, "A", "sha-a"); err != nil {
		t.Fatalf("integrate A: %v", err)
	}
	ready := readyKeys(t, gdb, e.ID)
	if !ready["B"] || !ready["C"] || ready["D"] {
		t.Fatalf("after A: %v", ready)
	}

	if err := RecordIntegration(gdb, e.ID, "B", "sha-b"); err != nil {
		t.Fatalf("integrate B: %v", err)
	}
	if ready := readyKeys(t, gdb, e.ID); ready["D"] {
		t.Fatalf("D ready with only one predecessor integrated: %v", ready)
	}
	if err := RecordIntegration(gdb, e.ID, "C", "sha-c"); err != nil {
		t.Fatalf("integrate C: %v", err)
	}
	if ready := readyKeys(t, gdb, e.ID); !ready["D"] {
		t.Fatalf("D not ready after both predecessors: %v", ready)
	}

	if err := RecordIntegration(gdb, e.ID, "D", "sha-d"); err != nil {
		t.Fatalf("integrate D: %v", err)
	}
	assertChildPhase(t, gdb, e.ID, "A", "closed")
	assertChildPhase(t, gdb, e.ID, "B", "closed")
	assertChildPhase(t, gdb, e.ID, "C", "closed")
	assertChildPhase(t, gdb, e.ID, "D", "closed")
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.State != "awaiting-final-review" {
		t.Fatalf("state after last receipt = %q", fresh.State)
	}
}

func TestRecordIntegrationFailsClosed(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := approvedDiamond(t, gdb)
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Unknown node key.
	if err := RecordIntegration(gdb, e.ID, "ZZ", "sha"); err == nil {
		t.Fatal("unknown node accepted")
	}
	// Missing SHA.
	if err := RecordIntegration(gdb, e.ID, "A", ""); err == nil {
		t.Fatal("empty SHA accepted")
	}
	// Identical repeat is one receipt; a conflicting repeat is an error.
	if err := RecordIntegration(gdb, e.ID, "A", "sha-a"); err != nil {
		t.Fatalf("integrate A: %v", err)
	}
	if err := gdb.Model(&db.Ticket{}).Where("epic_id = ? AND epic_node_key = ?", e.ID, "A").Update("phase", "ready-for-review").Error; err != nil {
		t.Fatalf("simulate legacy integrated phase: %v", err)
	}
	if err := RecordIntegration(gdb, e.ID, "A", "sha-a"); err != nil {
		t.Fatalf("identical repeat: %v", err)
	}
	assertChildPhase(t, gdb, e.ID, "A", "closed")
	if err := RecordIntegration(gdb, e.ID, "A", "sha-other"); !errors.Is(err, ErrIntegrationConflict) {
		t.Fatalf("conflicting repeat = %v", err)
	}
	// No receipts before the run starts: nothing is materialized, so the
	// node itself is unknown.
	e2 := createEpic(t, gdb)
	if _, err := AddRevision(gdb, e2.ID, diamondPlan(e2.IntegrationRef), ""); err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	if err := RecordIntegration(gdb, e2.ID, "A", "sha"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("receipt before start = %v", err)
	}
}

// Legacy rows migrate forward: a pre-epic ticket reads back with no epic
// fields and is indistinguishable from any ordinary ticket.
func TestLegacyTicketsHaveNoEpicFields(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	legacy := db.Ticket{ID: "legacy-1", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b", Title: "t", Description: "d", Phase: "unassigned"}
	if err := gdb.Create(&legacy).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got db.Ticket
	if err := gdb.First(&got, "id = 'legacy-1'").Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.EpicID != nil || got.EpicNodeKey != "" || got.EpicPlanRevision != 0 || got.EpicReady {
		t.Fatalf("legacy ticket gained epic fields: %+v", got)
	}
}

// The tester gate is a ticket-level flag the run composes: children of a
// run that requires the tester are materialized with the flag set, as a
// snapshot — like ManualApproval, a later policy edit never reaches them.
func TestStartSnapshotsTesterFlag(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Auto-merged",
		SpecMarkdown: "spec", AutoMergeChildren: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !e.TesterRequired {
		t.Fatal("precondition: auto-merge forces the tester requirement")
	}
	if _, err := AddRevision(gdb, e.ID, diamondPlan(e.IntegrationRef), ""); err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	_, rev, err := LoadPlan(gdb, e.ID, 1)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if err := Approve(gdb, e.ID, 1, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	children := epicChildren(t, gdb, e.ID)
	if len(children) == 0 {
		t.Fatal("no children")
	}
	for _, c := range children {
		if !c.TesterRequired {
			t.Fatalf("child %s materialized without the tester flag", c.ID)
		}
	}
	// The snapshot is the override: weakening the run's policy afterwards
	// leaves the children's gate alone.
	if err := gdb.Model(&db.Epic{}).Where("id = ?", e.ID).Update("tester_required", false).Error; err != nil {
		t.Fatal(err)
	}
	for _, c := range epicChildren(t, gdb, e.ID) {
		if !c.TesterRequired {
			t.Fatalf("policy edit reached child %s", c.ID)
		}
	}
}

// A run without auto-merge leaves its children on the default: no tester
// stage, exactly like an ordinary ticket.
func TestStartDefaultsChildrenToNoTester(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := approvedDiamond(t, gdb)
	if e.TesterRequired {
		t.Fatal("precondition: no auto-merge, no tester requirement")
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, c := range epicChildren(t, gdb, e.ID) {
		if c.TesterRequired {
			t.Fatalf("child %s gained the tester gate", c.ID)
		}
	}
}
