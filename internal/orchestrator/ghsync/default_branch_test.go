package ghsync_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// failingDefaultBranchGH answers DefaultBranch with an error (or an empty
// name) for the first n calls, then behaves like the wrapped fake. The fake's
// own FailNext lever cannot be used here: it fires on whichever method is
// called first, which for an ingest pass is ListIssuesSince.
type failingDefaultBranchGH struct {
	*github.Fake
	remaining int
	err       error
	empty     bool
	calls     int
}

func (g *failingDefaultBranchGH) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	g.calls++
	if g.remaining > 0 {
		g.remaining--
		if g.empty {
			return "", nil
		}
		return "", g.err
	}
	return g.Fake.DefaultBranch(ctx, owner, repo)
}

// TestCreateTicketUsesTheRepositoryDefaultBranch is the evidence that
// DefaultBranch is consulted at all. Mutation M21 — deleting the call and
// hardcoding "main" — passed the entire suite, because nothing anywhere
// asserted a ticket's base_branch against a repository whose default is not
// "main".
//
// base_branch is not cosmetic: enqueuePRIfReady takes it verbatim into the
// pull request, so a wrong value either 422s every attempt until the outbox
// row parks, or opens the pull request into a non-default branch where
// "Closes #N" silently never fires.
func TestCreateTicketUsesTheRepositoryDefaultBranch(t *testing.T) {
	for _, defaultBranch := range []string{"main", "master", "develop", "trunk"} {
		t.Run(defaultBranch, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			repo := newRepo(t, gdb)
			f := github.NewFake()
			f.Default = defaultBranch
			f.AddIssue(github.Issue{Number: 7, Title: "t", Body: "b", State: "open",
				Labels: []string{"golem"}, UpdatedAt: time.Now()})

			if err := ghsync.NewSyncer(gdb, f).IngestRepo(context.Background(), repo); err != nil {
				t.Fatalf("IngestRepo: %v", err)
			}

			var ticket db.Ticket
			if err := gdb.First(&ticket, "issue_number = ?", 7).Error; err != nil {
				t.Fatalf("ingested ticket: %v", err)
			}
			if ticket.BaseBranch != defaultBranch {
				t.Errorf("base_branch = %q, want %q — the pull request would target the wrong branch",
					ticket.BaseBranch, defaultBranch)
			}
		})
	}
}

// TestCreateTicketRetriesWhenDefaultBranchFails covers carry-forward item 5.
// createTicketFromIssue silently fell back to "main" on any DefaultBranch
// failure, so one transient 403 or secondary rate limit at the moment an
// issue was first ingested permanently pinned that ticket's base_branch to
// "main" — with the error not logged, not recorded on the repo row, not
// retried and not surfaced anywhere.
//
// The failure now propagates into the existing partial-failure guard, which
// leaves the cursor and the ETag where they are, so the next poll retries the
// issue and gets the right branch. It costs one poll interval for that one
// issue, which is the correct price for a transient API error.
//
// Verified against HEAD f834145 before the fix: a ticket was created
// immediately with base_branch="main" and repo.LastError was empty.
func TestCreateTicketRetriesWhenDefaultBranchFails(t *testing.T) {
	cases := []struct {
		name  string
		empty bool
	}{
		{name: "DefaultBranch returns an error"},
		{name: "DefaultBranch returns an empty name", empty: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gdb, err := db.Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			repo := newRepo(t, gdb)
			f := github.NewFake()
			f.Default = "develop"
			f.AddIssue(github.Issue{Number: 7, Title: "t", Body: "b", State: "open",
				Labels: []string{"golem"}, UpdatedAt: time.Now()})
			gh := &failingDefaultBranchGH{
				Fake: f, remaining: 1, empty: tc.empty,
				err: errors.New("403 secondary rate limit"),
			}

			s := ghsync.NewSyncer(gdb, gh)
			// The pass itself is not an error to the caller — one bad issue
			// must not abandon the rest of the page — but it must be
			// recorded and the cursor must not move.
			if err := s.IngestRepo(context.Background(), repo); err != nil {
				t.Fatalf("IngestRepo: %v", err)
			}

			var n int64
			gdb.Model(&db.Ticket{}).Count(&n)
			if n != 0 {
				var got db.Ticket
				gdb.First(&got)
				t.Fatalf("ticket count = %d (base_branch=%q), want 0 — a ticket must not be created with a guessed base branch",
					n, got.BaseBranch)
			}

			var after db.GitHubRepo
			if err := gdb.First(&after, repo.ID).Error; err != nil {
				t.Fatalf("reload repo: %v", err)
			}
			if !strings.Contains(strings.ToLower(after.LastError), "default branch") {
				t.Errorf("LastError = %q, want it to name the default-branch lookup", after.LastError)
			}
			if after.LastIssueSync != nil {
				t.Errorf("LastIssueSync = %v, want nil — the cursor must not move past an issue that was never applied",
					*after.LastIssueSync)
			}

			// The next poll retries the issue and gets the real branch.
			if err := s.IngestRepo(context.Background(), repo); err != nil {
				t.Fatalf("second IngestRepo: %v", err)
			}
			var ticket db.Ticket
			if err := gdb.First(&ticket, "issue_number = ?", 7).Error; err != nil {
				t.Fatalf("ticket after retry: %v", err)
			}
			if ticket.BaseBranch != "develop" {
				t.Errorf("base_branch = %q, want develop", ticket.BaseBranch)
			}
			var recovered db.GitHubRepo
			gdb.First(&recovered, repo.ID)
			if recovered.LastError != "" {
				t.Errorf("LastError = %q, want cleared after the successful retry", recovered.LastError)
			}
		})
	}
}
