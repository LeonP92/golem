package ghsync_test

import (
	"context"
	"strings"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// seedFinalPREpic returns a running two-node epic on a registered, enabled
// repository — the shape EnqueueFinalPR fires for.
func seedFinalPREpic(t *testing.T, gdb *gorm.DB) *db.Epic {
	t.Helper()
	if err := gdb.Create(&db.GitHubRepo{
		RepoRemote: "https://github.com/org/repo", Owner: "org", Name: "repo",
		Label: "golem", Enabled: true,
	}).Error; err != nil {
		t.Fatalf("repo: %v", err)
	}
	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "x", SpecMarkdown: "s",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
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
	return e
}

// The completing receipt queues exactly one final pull request — for the
// integration branch, not a child — and the drain opens it and records it
// on the epic.
func TestFinalPRQueuedOnLastReceipt(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := seedFinalPREpic(t, gdb)

	if err := epics.RecordIntegration(gdb, e.ID, "A", "m1", ghsync.EnqueueFinalPR); err != nil {
		t.Fatalf("receipt A: %v", err)
	}
	var queued int64
	gdb.Model(&db.GitHubOutbox{}).Where("kind = ? AND epic_id = ?", ghsync.KindPR, e.ID).Count(&queued)
	if queued != 0 {
		t.Fatal("final PR queued before the run was fully integrated")
	}
	if err := epics.RecordIntegration(gdb, e.ID, "B", "m2", ghsync.EnqueueFinalPR); err != nil {
		t.Fatalf("receipt B: %v", err)
	}
	gdb.Model(&db.GitHubOutbox{}).Where("kind = ? AND epic_id = ?", ghsync.KindPR, e.ID).Count(&queued)
	if queued != 1 {
		t.Fatalf("queued = %d, want 1", queued)
	}
	// A duplicate receipt (retry after a crash) does not queue a second.
	if err := epics.RecordIntegration(gdb, e.ID, "B", "m2", ghsync.EnqueueFinalPR); err != nil {
		t.Fatalf("repeat receipt B: %v", err)
	}
	gdb.Model(&db.GitHubOutbox{}).Where("kind = ? AND epic_id = ?", ghsync.KindPR, e.ID).Count(&queued)
	if queued != 1 {
		t.Fatalf("queued after repeat = %d, want 1", queued)
	}

	var row db.GitHubOutbox
	gdb.First(&row, "kind = ? AND epic_id = ?", ghsync.KindPR, e.ID)
	if !strings.Contains(row.Payload, e.IntegrationRef) || !strings.Contains(row.Payload, e.BaseBranch) {
		t.Fatalf("payload = %s", row.Payload)
	}

	f := github.NewFake()
	if err := ghsync.NewSyncer(gdb, f).Drain(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.FinalPRNumber == nil || *fresh.FinalPRNumber != 100 || fresh.FinalPRURL == "" {
		t.Fatalf("epic final pr: %+v", fresh)
	}
}

// Without an enabled repository for the run's remote the hook is a no-op:
// the core flow is identical with or without the add-on.
func TestFinalPRSkippedWithoutEnabledRepo(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := seedFinalPREpic(t, gdb)
	gdb.Model(&db.GitHubRepo{}).Where("repo_remote = ?", e.RepoRemote).Update("enabled", false)

	for _, node := range []string{"A", "B"} {
		if err := epics.RecordIntegration(gdb, e.ID, node, "m-"+node, ghsync.EnqueueFinalPR); err != nil {
			t.Fatalf("receipt %s: %v", node, err)
		}
	}
	var fresh db.Epic
	gdb.First(&fresh, "id = ?", e.ID)
	if fresh.State != epics.StateAwaitingFinalReview {
		t.Fatalf("state = %q, want awaiting-final-review", fresh.State)
	}
	var queued int64
	gdb.Model(&db.GitHubOutbox{}).Where("kind = ? AND epic_id = ?", ghsync.KindPR, e.ID).Count(&queued)
	if queued != 0 {
		t.Fatal("final PR queued with no enabled repository")
	}
}
