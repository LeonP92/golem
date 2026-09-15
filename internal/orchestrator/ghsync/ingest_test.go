package ghsync_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

// newRepo seeds an enabled GitHubRepo and returns it.
func newRepo(t *testing.T, gdb *gorm.DB) *db.GitHubRepo {
	t.Helper()
	r := db.GitHubRepo{
		RepoRemote: "https://github.com/org/repo",
		Owner:      "org", Name: "repo", Enabled: true, Label: "golem",
	}
	if err := gdb.Create(&r).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return &r
}

func TestIngest(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name        string
		seedTicket  *db.Ticket
		issue       github.Issue
		wantTickets int64
		wantPhase   string
		wantTitle   string
	}{
		{
			name: "new labeled issue creates an unassigned ticket",
			issue: github.Issue{Number: 7, Title: "Add rate limiting", Body: "details",
				State: "open", HTMLURL: "https://github.com/org/repo/issues/7",
				UpdatedAt: now, Labels: []string{"golem"}},
			wantTickets: 1,
			wantPhase:   "unassigned",
			wantTitle:   "Add rate limiting",
		},
		{
			name: "existing ticket takes the issue title, keeps its phase",
			seedTicket: &db.Ticket{ID: "t1", RepoRemote: "https://github.com/org/repo",
				Title: "old title", Branch: "ticket/old-t1", Description: "old",
				Phase: "implement", IssueNumber: intPtr(7)},
			issue: github.Issue{Number: 7, Title: "new title", Body: "new body",
				State: "open", UpdatedAt: now, Labels: []string{"golem"}},
			wantTickets: 1,
			wantPhase:   "implement",
			wantTitle:   "new title",
		},
		{
			name: "closed issue closes the ticket",
			seedTicket: &db.Ticket{ID: "t1", RepoRemote: "https://github.com/org/repo",
				Title: "t", Branch: "ticket/t-t1", Description: "d",
				Phase: "implement", IssueNumber: intPtr(7)},
			issue: github.Issue{Number: 7, Title: "t", State: "closed",
				UpdatedAt: now, Labels: []string{"golem"}},
			wantTickets: 1,
			wantPhase:   "closed",
			wantTitle:   "t",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			repo := newRepo(t, gdb)
			if tt.seedTicket != nil {
				if err := gdb.Create(tt.seedTicket).Error; err != nil {
					t.Fatalf("seed ticket: %v", err)
				}
			}

			f := github.NewFake()
			f.AddIssue(tt.issue)

			s := ghsync.NewSyncer(gdb, f)
			if err := s.IngestRepo(context.Background(), repo); err != nil {
				t.Fatalf("IngestRepo: %v", err)
			}

			var n int64
			gdb.Model(&db.Ticket{}).Count(&n)
			if n != tt.wantTickets {
				t.Fatalf("ticket count = %d, want %d", n, tt.wantTickets)
			}
			var got db.Ticket
			if err := gdb.First(&got).Error; err != nil {
				t.Fatalf("load ticket: %v", err)
			}
			if got.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Phase, tt.wantPhase)
			}
			if got.Title != tt.wantTitle {
				t.Errorf("title = %q, want %q", got.Title, tt.wantTitle)
			}
			if got.IssueNumber == nil || *got.IssueNumber != 7 {
				t.Errorf("issue linkage not set: %+v", got.IssueNumber)
			}
		})
	}
}

func TestIngestIsIdempotentAcrossOverlappingPolls(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		// Reset the cursor each pass to force the overlap the real cursor
		// deliberately creates.
		repo.LastIssueSync = nil
		if err := s.IngestRepo(ctx, repo); err != nil {
			t.Fatalf("IngestRepo pass %d: %v", i, err)
		}
	}
	var n int64
	gdb.Model(&db.Ticket{}).Count(&n)
	if n != 1 {
		t.Errorf("ticket count = %d after 3 overlapping passes, want 1", n)
	}
}

func TestIngestSkipsUnlabeledIssues(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 9, Title: "unrelated", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"bug"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}
	var n int64
	gdb.Model(&db.Ticket{}).Count(&n)
	if n != 0 {
		t.Errorf("ticket count = %d, want 0 — unlabeled issue was ingested", n)
	}
}

func TestIngestAdvancesCursorWithOverlap(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	updated := time.Now().Truncate(time.Second)
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		UpdatedAt: updated, Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}

	var got db.GitHubRepo
	if err := gdb.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if got.LastIssueSync == nil {
		t.Fatal("LastIssueSync not advanced")
	}
	want := updated.Add(-time.Minute)
	if got.LastIssueSync.Sub(want).Abs() > time.Second {
		t.Errorf("LastIssueSync = %v, want ~%v (max updated_at minus 1m overlap)",
			got.LastIssueSync, want)
	}
	if got.LastPolledAt == nil {
		t.Error("LastPolledAt not set")
	}
}

// TestIngestRecordsGitHubErrorAndReturnsIt is not part of the task brief's
// prescribed test list; it was added to exercise the GitHub-error path
// (IngestRepo's early return and recordRepoError), which the brief's four
// tests never reach and which otherwise had zero coverage.
func TestIngestRecordsGitHubErrorAndReturnsIt(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	f := github.NewFake()
	f.FailNext = errors.New("boom")

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err == nil {
		t.Fatal("IngestRepo: want error, got nil")
	}

	var got db.GitHubRepo
	if err := gdb.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if got.LastError == "" {
		t.Error("LastError not recorded on repo")
	}
	var n int64
	gdb.Model(&db.Ticket{}).Count(&n)
	if n != 0 {
		t.Errorf("ticket count = %d, want 0 — a list error must not create tickets", n)
	}
}

// TestIngestPartialFailureDoesNotAdvanceCursor covers fix-round-1 item 1: a
// page with two issues where the older one's ticket write fails must not
// advance the cursor at all, or the failed issue would be permanently
// excluded from every future poll (since = max(updated_at) - overlap would
// sit past it). The failure must also surface in repo.LastError.
//
// A SQLite trigger gives a deterministic, non-flaky write failure for one
// specific issue's ticket insert — the alternative of racing two goroutines
// against the (repo_remote, issue_number) unique index would be flaky and
// wasn't attempted.
func TestIngestPartialFailureDoesNotAdvanceCursor(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := gdb.Exec(`
CREATE TRIGGER golem_test_fail_insert
BEFORE INSERT ON tickets
WHEN NEW.title = 'boom-title'
BEGIN
    SELECT RAISE(ABORT, 'simulated write failure');
END;
`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	repo := newRepo(t, gdb)

	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 1, Title: "boom-title", State: "open",
		UpdatedAt: older, Labels: []string{"golem"}})
	f.AddIssue(github.Issue{Number: 2, Title: "ok-title", State: "open",
		UpdatedAt: newer, Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}

	var got db.GitHubRepo
	if err := gdb.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if got.LastIssueSync != nil {
		t.Errorf("LastIssueSync = %v, want nil (unmoved) — a partial failure must not advance the cursor", *got.LastIssueSync)
	}
	if got.LastError == "" {
		t.Error("LastError not recorded after a partial failure — the stall would be invisible to an operator")
	}
	if got.LastPolledAt == nil {
		t.Error("LastPolledAt not set after a partial failure")
	}

	if err := gdb.Where("title = ?", "ok-title").First(&db.Ticket{}).Error; err != nil {
		t.Errorf("ticket for the succeeding issue was not created: %v", err)
	}
	var n int64
	gdb.Model(&db.Ticket{}).Where("title = ?", "boom-title").Count(&n)
	if n != 0 {
		t.Errorf("ticket count for the failing issue = %d, want 0", n)
	}
}

// TestIngestSkipsProcessingWhenNotModified covers fix-round-1 item 2: a 304
// (simulated via github.Fake's ETag field) must create or modify no tickets,
// must still update LastPolledAt and clear LastError, and — the part that
// most needs a regression test — must NOT move the cursor.
func TestIngestSkipsProcessingWhenNotModified(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	repo := newRepo(t, gdb)
	cursor := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := gdb.Model(repo).Updates(map[string]any{
		"last_issue_sync": cursor, "etag": "match-me",
	}).Error; err != nil {
		t.Fatalf("seed cursor/etag: %v", err)
	}
	repo.LastIssueSync = &cursor
	repo.ETag = "match-me"

	f := github.NewFake()
	f.ETag = "match-me"
	// An issue exists but must never be read: NotModified short-circuits
	// before any issue is processed.
	f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	s := ghsync.NewSyncer(gdb, f)
	if err := s.IngestRepo(context.Background(), repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}

	var n int64
	gdb.Model(&db.Ticket{}).Count(&n)
	if n != 0 {
		t.Errorf("ticket count = %d, want 0 — NotModified must skip processing", n)
	}

	var got db.GitHubRepo
	if err := gdb.First(&got, repo.ID).Error; err != nil {
		t.Fatalf("reload repo: %v", err)
	}
	if got.LastPolledAt == nil {
		t.Error("LastPolledAt not set on a NotModified poll")
	}
	if got.LastError != "" {
		t.Errorf("LastError = %q, want empty after a clean NotModified poll", got.LastError)
	}
	if got.LastIssueSync == nil || got.LastIssueSync.Sub(cursor).Abs() > time.Second {
		t.Errorf("LastIssueSync = %v, want unchanged at ~%v — a NotModified poll must not move the cursor",
			got.LastIssueSync, cursor)
	}
}

func intPtr(n int) *int { return &n }
