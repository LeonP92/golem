package ghsync_test

import (
	"context"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// seedEpicPRTicket runs a two-node epic (A -> B) and makes the seeded PR
// ticket t1 its child A.
func seedEpicPRTicket(t *testing.T, gdb *gorm.DB, autoMerge bool) *db.Epic {
	t.Helper()
	seedPRTicket(t, gdb, "ready-for-review", 0)
	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "x", SpecMarkdown: "s", AutoMergeChildren: autoMerge,
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	plan := epic.Plan{
		Requirements: []epic.Requirement{{ID: "R1", Text: "a"}, {ID: "R2", Text: "b"}},
		Nodes: []epic.Node{
			{Key: "A", Title: "first", AcceptanceCommands: []string{"t"}, RequirementIDs: []string{"R1"}},
			{Key: "B", Title: "second", AcceptanceCommands: []string{"t"}, RequirementIDs: []string{"R2"}},
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
	gdb.Exec("DELETE FROM tickets WHERE epic_id = ? AND epic_node_key = 'A'", e.ID)
	if err := gdb.Model(&db.Ticket{}).Where("id = 't1'").Updates(map[string]any{
		"epic_id": e.ID, "epic_node_key": "A", "epic_plan_revision": 1, "epic_ready": true,
		"base_branch": e.IntegrationRef, "issue_number": nil,
	}).Error; err != nil {
		t.Fatalf("adopt ticket: %v", err)
	}
	return e
}

// Epic children carry no pull requests in the current flow — the shem
// that did the work merges into the run's target ref directly — so the
// monitor leaves every child alone, even one that still has a pr_number
// from before: no receipt, no bounce, no close.
func TestMonitorPRs_SkipsEpicChildren(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := seedEpicPRTicket(t, gdb, true)
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {
		Number: 42, State: "closed", Merged: true,
		HeadSHA: "abc123", BaseRef: e.IntegrationRef, MergeCommitSHA: "merge999",
	}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())

	tk := reload(t, gdb)
	if tk.Phase != "ready-for-review" || tk.EpicIntegratedSHA != "" {
		t.Fatalf("child touched: phase = %q receipt = %q", tk.Phase, tk.EpicIntegratedSHA)
	}
	if len(f.CallsSnapshot()) != 0 {
		t.Fatalf("the monitor called GitHub for a child: %v", f.CallsSnapshot())
	}
}

// A paused run's child is left alone: dispatching a fix would strand it in
// revising, because revise claims are refused until the run resumes.
func TestMonitorPRs_PausedEpicChildIsNotDispatched(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := seedEpicPRTicket(t, gdb, false)
	if err := epics.Pause(gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{42: {Number: 42, State: "open", HeadSHA: "abc123", BaseRef: e.IntegrationRef}}
	f.FailedChecks = map[string][]github.CheckFailure{"abc123": {{Name: "Test", Conclusion: "failure"}}}
	ghsync.NewSyncer(gdb, f).MonitorPullRequests(context.Background())
	if tk := reload(t, gdb); tk.Phase != "ready-for-review" {
		t.Fatalf("phase = %q, want ready-for-review while paused", tk.Phase)
	}
}
