package ghsync_test

import (
	"context"
	"testing"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// A merged child PR is the run learning that a node integrated: the
// monitor closes the ticket AND writes the receipt that unblocks the
// successor.
func TestMonitorPRs_MergedChildWritesIntegrationReceipt(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)

	// A two-node run whose first child is the PR ticket.
	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "x", SpecMarkdown: "s",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	plan := epic.Plan{
		Requirements: []epic.Requirement{{ID: "R1", Text: "a"}, {ID: "R2", Text: "b"}},
		Nodes: []epic.Node{
			{Key: "A", Title: "first", AcceptanceCommands: []string{"t"},
				RequirementIDs: []string{"R1"}, BranchTarget: e.IntegrationRef},
			{Key: "B", Title: "second", AcceptanceCommands: []string{"t"},
				RequirementIDs: []string{"R2"}, BranchTarget: e.IntegrationRef},
		},
		Edges: []epic.Edge{{Predecessor: "A", Successor: "B", Reason: "b needs a"}},
	}
	rev, err := epics.AddRevision(gdb, e.ID, plan, "")
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	if err := epics.Approve(gdb, e.ID, rev.Revision, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := epics.Start(gdb, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Make the seeded PR ticket the materialized child A: remove the
	// placeholder Start created for the node, then adopt t1 in its place.
	if err := gdb.Exec("DELETE FROM tickets WHERE epic_id = ? AND epic_node_key = 'A'", e.ID).Error; err != nil {
		t.Fatalf("remove placeholder: %v", err)
	}
	if err := gdb.Model(&db.Ticket{}).Where("id = 't1'").Updates(map[string]any{
		"epic_id": e.ID, "epic_node_key": "A", "epic_plan_revision": 1, "epic_ready": true,
	}).Error; err != nil {
		t.Fatalf("adopt ticket: %v", err)
	}
	// Start's own child row for A must not confuse the receipt path:
	// point the node mapping at the real PR ticket, and remove the
	// materialized duplicate for the purposes of this monitor test.
	gdb.Model(&db.EpicNode{}).Where("epic_id = ? AND node_key = 'A'", e.ID).Update("ticket_id", "t1")

	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: true,
		HeadSHA: "abc123", BaseRef: e.IntegrationRef, MergeCommitSHA: "merge999",
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	if tk := reload(t, gdb); tk.Phase != "closed" {
		t.Fatalf("phase = %q, want closed", tk.Phase)
	}
	var node db.EpicNode
	if err := gdb.First(&node, "epic_id = ? AND node_key = 'A'", e.ID).Error; err != nil {
		t.Fatalf("node: %v", err)
	}
	if node.IntegratedSHA != "merge999" || node.IntegratedRef != e.IntegrationRef {
		t.Fatalf("receipt = %q into %q", node.IntegratedSHA, node.IntegratedRef)
	}
	// The successor's readiness flipped on.
	var childB db.Ticket
	if err := gdb.First(&childB, "epic_id = ? AND epic_node_key = 'B'", e.ID).Error; err != nil {
		t.Fatalf("child B: %v", err)
	}
	if !childB.EpicReady {
		t.Fatal("successor not unblocked by the receipt")
	}
}

func TestMonitorPRs_MergedOrdinaryTicketWritesNoReceipt(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: true, HeadSHA: "abc123", BaseRef: "main",
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())
	if tk := reload(t, gdb); tk.Phase != "closed" {
		t.Fatalf("phase = %q, want closed", tk.Phase)
	}
	var n int64
	gdb.Model(&db.EpicNode{}).Where("integrated_sha <> ''").Count(&n)
	if n != 0 {
		t.Fatalf("%d receipts from an ordinary ticket", n)
	}
}

// A paused run's child is left alone: dispatching a fix would strand it in
// revising, because revise claims are refused until the run resumes.
func TestMonitorPRs_PausedEpicChildIsNotDispatched(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedPRTicket(t, gdb, "ready-for-review", 0)
	gdb.Create(&db.Epic{ID: "e-paused", RepoRemote: "https://github.com/org/repo", Title: "x", State: "paused"})
	gdb.Model(&db.Ticket{}).Where("id = 't1'").Updates(map[string]any{"epic_id": "e-paused", "epic_node_key": "A"})
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {Number: 42, State: "open", HeadSHA: "abc123", BaseRef: "main"}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())
	if tk := reload(t, gdb); tk.Phase != "ready-for-review" {
		t.Fatalf("phase = %q, want ready-for-review while paused", tk.Phase)
	}
}
