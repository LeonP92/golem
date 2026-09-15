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

func intPtr(n int) *int { return &n }
