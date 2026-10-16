package epics

import (
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

func validPlan() epic.Plan {
	return epic.Plan{
		Requirements: []epic.Requirement{{ID: "R1", Text: "model"}, {ID: "R2", Text: "api"}},
		Nodes: []epic.Node{
			{Key: "T1", Title: "schema", AcceptanceCommands: []string{"go test ./..."},
				RequirementIDs: []string{"R1"}, BranchTarget: "epic/x"},
			{Key: "T2", Title: "api", AcceptanceCommands: []string{"go test ./api/..."},
				RequirementIDs: []string{"R2"}, BranchTarget: "epic/x"},
		},
		Edges: []epic.Edge{{Predecessor: "T1", Successor: "T2", Reason: "api uses model"}},
	}
}

func createEpic(t *testing.T, gdb *gorm.DB) *db.Epic {
	t.Helper()
	e, err := Create(gdb, CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Workspaces",
		SpecMarkdown: "# Workspaces\n\nR1 model\nR2 api\n",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return e
}

func TestCreateDefaultsAndIntegrationRef(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	if e.State != "draft" || e.BranchStrategy != StrategyEpicBranch || e.DeliveryTarget != TargetFeatureBranch {
		t.Errorf("defaults wrong: %+v", e)
	}
	if e.IntegrationRef != "epic/"+e.ID {
		t.Errorf("IntegrationRef = %q, want epic/<id>", e.IntegrationRef)
	}
	if e.SpecHash == "" {
		t.Error("spec not hashed")
	}
}

func TestCreateRejectsTrunkWithFeatureBranchTarget(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	_, err := Create(gdb, CreateParams{
		RepoRemote: "r", Title: "t", SpecMarkdown: "s",
		BranchStrategy: StrategyTrunkFlags, DeliveryTarget: TargetFeatureBranch,
	})
	if err == nil {
		t.Error("trunk-flags with a feature-branch target must be rejected")
	}
	e, err := Create(gdb, CreateParams{
		RepoRemote: "r", Title: "t", SpecMarkdown: "s",
		BranchStrategy: StrategyTrunkFlags, DeliveryTarget: TargetBaseBranch,
	})
	if err != nil {
		t.Fatalf("trunk-flags with base target: %v", err)
	}
	if e.IntegrationRef != "" {
		t.Errorf("trunk-flags must not get an integration ref, got %q", e.IntegrationRef)
	}
}

// An invalid plan never reaches the database: cycles, invalid IDs and
// coverage gaps are rejected with every problem named.
func TestAddRevisionRejectsInvalidPlan(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	p := validPlan()
	p.Edges = append(p.Edges, epic.Edge{Predecessor: "T2", Successor: "T1", Reason: "loop"})
	if _, err := AddRevision(gdb, e.ID, p, "sha1"); err == nil {
		t.Fatal("cyclic plan accepted")
	}
	var count int64
	gdb.Model(&db.EpicPlanRevision{}).Count(&count)
	if count != 0 {
		t.Errorf("invalid plan left %d revisions behind", count)
	}
	gdb.Model(&db.EpicNode{}).Count(&count)
	if count != 0 {
		t.Errorf("invalid plan left %d nodes behind", count)
	}
}

// The stored revision round-trips into the same plan: the DAG view and the
// list view read one record and cannot drift.
func TestAddRevisionRoundTripsAndSnapshots(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	rev, err := AddRevision(gdb, e.ID, validPlan(), "sha1")
	if err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	if rev.Revision != 1 {
		t.Errorf("Revision = %d, want 1", rev.Revision)
	}
	plan, _, err := LoadPlan(gdb, e.ID, 1)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}
	if len(plan.Nodes) != 2 || len(plan.Edges) != 1 || plan.Nodes[0].Key != "T1" {
		t.Errorf("round-tripped plan differs: %+v", plan)
	}
	var nodes, edges int64
	gdb.Model(&db.EpicNode{}).Count(&nodes)
	gdb.Model(&db.EpicEdge{}).Count(&edges)
	if nodes != 2 || edges != 1 {
		t.Errorf("snapshots: %d nodes %d edges, want 2/1", nodes, edges)
	}
	var updated db.Epic
	gdb.Where("id = ?", e.ID).First(&updated)
	if updated.State != "awaiting-approval" {
		t.Errorf("state = %q, want awaiting-approval", updated.State)
	}
}

// Approving binds to the exact revision hash; a wrong hash fails.
func TestApproveBindsToExactHash(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	rev, _ := AddRevision(gdb, e.ID, validPlan(), "sha1")
	if err := Approve(gdb, e.ID, rev.Revision, "wrong-hash", "leon"); err != ErrHashMismatch {
		t.Errorf("approve with wrong hash: %v, want ErrHashMismatch", err)
	}
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	var updated db.Epic
	gdb.Where("id = ?", e.ID).First(&updated)
	if updated.ApprovedRevision != 1 || updated.ApprovedPlanHash != rev.PlanHash {
		t.Errorf("approval not recorded on the epic: %+v", updated)
	}
}

// The M1 exit invariant: editing approved content invalidates the approval.
func TestEditingApprovedContentInvalidatesApproval(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	rev, _ := AddRevision(gdb, e.ID, validPlan(), "sha1")
	if err := Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("Approve: %v", err)
	}

	p := validPlan()
	p.Nodes[0].Title = "schema, revised"
	rev2, err := AddRevision(gdb, e.ID, p, "sha2")
	if err != nil {
		t.Fatalf("second AddRevision: %v", err)
	}
	if rev2.Revision != 2 {
		t.Errorf("Revision = %d, want 2", rev2.Revision)
	}
	var updated db.Epic
	gdb.Where("id = ?", e.ID).First(&updated)
	if updated.ApprovedRevision != 0 || updated.ApprovedPlanHash != "" {
		t.Errorf("edit kept the old approval: %+v", updated)
	}
	// The old revision is untouched history, with its approval still on it.
	_, old, err := LoadPlan(gdb, e.ID, 1)
	if err != nil {
		t.Fatalf("LoadPlan revision 1: %v", err)
	}
	if old.ApprovedBy != "leon" {
		t.Errorf("revision 1 lost its historical approval record")
	}
}

func TestCreateAutoMergePolicy(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	// Final auto-merge is only meaningful for a base-branch delivery
	// target: a feature-branch handoff is always human-merged.
	_, err := Create(gdb, CreateParams{
		RepoRemote: "example.com/x/y", Title: "t", SpecMarkdown: "s",
		DeliveryTarget: TargetFeatureBranch, AutoMergeFinal: true,
	})
	if err == nil {
		t.Fatal("final auto-merge on feature-branch target must fail closed")
	}
	// Any auto-merge forces the tester gate on.
	e, err := Create(gdb, CreateParams{
		RepoRemote: "example.com/x/y", Title: "t", SpecMarkdown: "s",
		DeliveryTarget: TargetBaseBranch, AutoMergeChildren: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !e.TesterRequired || !e.AutoMergeChildren || e.AutoMergeFinal {
		t.Fatalf("policy fields: %+v", e)
	}
	// Defaults stay fully off for older rows/clients.
	d, err := Create(gdb, CreateParams{
		RepoRemote: "example.com/x/z", Title: "t", SpecMarkdown: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.AutoMergeChildren || d.AutoMergeFinal || d.TesterRequired {
		t.Fatal("auto-merge defaults must be off")
	}
}

// Phase auto-approval is inherited at materialization and snapshotted:
// children keep the mode they were created with, whatever happens to the
// run's policy afterwards, and a per-child override is the child's own.
func TestAutoApprovePhasesInheritance(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{
		RepoRemote: "example.com/x/y", Title: "t", SpecMarkdown: "s",
		AutoApprovePhases: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := AddRevision(gdb, e.ID, validPlan(), "")
	Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon")
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	var children []db.Ticket
	gdb.Where("epic_id = ?", e.ID).Find(&children)
	if len(children) == 0 {
		t.Fatal("no children")
	}
	for _, c := range children {
		if c.ManualApproval {
			t.Fatalf("child %s did not inherit auto-approval", c.ID)
		}
	}
	// A mid-run policy change never reaches existing children.
	gdb.Model(&db.Epic{}).Where("id = ?", e.ID).Update("auto_approve_phases", false)
	if _, err := Start(gdb, e.ID); err != nil { // idempotent restart
		t.Fatal(err)
	}
	for _, c := range children {
		var fresh db.Ticket
		gdb.First(&fresh, "id = ?", c.ID)
		if fresh.ManualApproval {
			t.Fatalf("child %s mode silently changed", c.ID)
		}
	}
	// Default policy materializes manual-approval children, as before.
	e2, _ := Create(gdb, CreateParams{RepoRemote: "example.com/x/z", Title: "t", SpecMarkdown: "s"})
	rev2, _ := AddRevision(gdb, e2.ID, validPlan(), "")
	Approve(gdb, e2.ID, rev2.Revision, rev2.PlanHash, "leon")
	Start(gdb, e2.ID)
	var c2 db.Ticket
	gdb.First(&c2, "epic_id = ?", e2.ID)
	if !c2.ManualApproval {
		t.Fatal("default policy must keep human approval per child")
	}
}

// Approval binds to the current revision of a run still awaiting it: a
// stale tab cannot approve a replaced revision, and nothing re-approves a
// live run out from under its children.
func TestApproveOnlyCurrentRevisionBeforeStart(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	r1, _ := AddRevision(gdb, e.ID, validPlan(), "")
	p2 := validPlan()
	p2.Nodes[0].Title = "schema v2"
	r2, _ := AddRevision(gdb, e.ID, p2, "")
	if err := Approve(gdb, e.ID, r1.Revision, r1.PlanHash, "leon"); err == nil {
		t.Fatal("approved a replaced revision")
	}
	if err := Approve(gdb, e.ID, r2.Revision, r2.PlanHash, "leon"); err != nil {
		t.Fatalf("approve current: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	if err := Approve(gdb, e.ID, r2.Revision, r2.PlanHash, "mallory"); err == nil {
		t.Fatal("re-approved a running epic")
	}
}
