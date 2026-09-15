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
	"gorm.io/gorm"
)

// seedLinkedTicket creates a repo plus a ticket linked to issue #7.
func seedLinkedTicket(t *testing.T, gdb *gorm.DB, phase string) {
	t.Helper()
	newRepo(t, gdb)
	tk := db.Ticket{ID: "t1", RepoRemote: "https://github.com/org/repo",
		Title: "Add rate limiting", Branch: "ticket/add-rate-limiting-t1",
		BaseBranch: "main", Description: "d", Phase: phase, IssueNumber: intPtr(7)}
	if err := gdb.Create(&tk).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
}

func TestDrainComment(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "plan")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindComment,
		Payload:        `{"body":"Plan approved."}`,
		IdempotencyKey: ghsync.CommentKey("t1", "plan-approved"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	s := ghsync.NewSyncer(gdb, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if got := f.CommentsFor(7); len(got) != 1 || got[0] != "Plan approved." {
		t.Fatalf("comments = %v, want one \"Plan approved.\"", got)
	}
	var row db.GitHubOutbox
	gdb.First(&row, "ticket_id = ?", "t1")
	if row.DoneAt == nil {
		t.Error("DoneAt nil after successful drain")
	}

	// A second drain must not repost.
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if got := len(f.CommentsFor(7)); got != 1 {
		t.Errorf("comment count = %d after second drain, want 1", got)
	}
}

func TestDrainLabelReplacesPriorPhaseLabel(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "implement")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open",
		Labels: []string{"golem", "golem:plan", "bug"}})

	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindLabel,
		Payload:        `{"phase":"implement"}`,
		IdempotencyKey: ghsync.LabelKey("t1", "implement"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	s := ghsync.NewSyncer(gdb, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	issue, _ := f.GetIssue(context.Background(), "org", "repo", 7)
	if !issue.HasLabel("golem:implement") {
		t.Error("golem:implement not applied")
	}
	if issue.HasLabel("golem:plan") {
		t.Error("stale golem:plan label not removed")
	}
	for _, keep := range []string{"golem", "bug"} {
		if !issue.HasLabel(keep) {
			t.Errorf("label %q outside the golem:* namespace was removed", keep)
		}
	}
}

func TestDrainCloseIssue(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "closed")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindClose, Payload: `{}`,
		IdempotencyKey: ghsync.CloseKey("t1"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	s := ghsync.NewSyncer(gdb, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	issue, _ := f.GetIssue(context.Background(), "org", "repo", 7)
	if issue.State != "closed" {
		t.Errorf("issue state = %q, want closed", issue.State)
	}
}

func TestDrainRetriesWithBackoffThenParks(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "plan")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindComment, Payload: `{"body":"x"}`,
		IdempotencyKey: ghsync.CommentKey("t1", "spec"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	s := ghsync.NewSyncer(gdb, f)

	// First failure: row stays undone, attempts=1, NextAttempt pushed out.
	f.SetFailNext(errors.New("503 from GitHub"))
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	var row db.GitHubOutbox
	gdb.First(&row, "ticket_id = ?", "t1")
	if row.DoneAt != nil {
		t.Error("DoneAt set despite failure")
	}
	if row.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", row.Attempts)
	}
	if !row.NextAttempt.After(time.Now()) {
		t.Error("NextAttempt not pushed into the future")
	}
	if row.LastError == "" {
		t.Error("LastError empty after failure")
	}

	// A drain before NextAttempt must skip the row entirely.
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	gdb.First(&row, "ticket_id = ?", "t1")
	if row.Attempts != 1 {
		t.Errorf("Attempts = %d after early drain, want 1 — backoff not honoured", row.Attempts)
	}

	// Drive it to the parking limit.
	gdb.Model(&db.GitHubOutbox{}).Where("ticket_id = ?", "t1").
		Updates(map[string]any{"attempts": ghsync.MaxAttempts, "next_attempt": time.Now().Add(-time.Hour)})
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	gdb.First(&row, "ticket_id = ?", "t1")
	if row.Attempts != ghsync.MaxAttempts {
		t.Errorf("Attempts = %d, want the row parked at %d", row.Attempts, ghsync.MaxAttempts)
	}
	if row.DoneAt != nil {
		t.Error("parked row marked done")
	}
}

func TestBackoffGrowsAndCaps(t *testing.T) {
	prev := time.Duration(0)
	for i := 1; i <= 10; i++ {
		got := ghsync.Backoff(i)
		if got < prev {
			t.Errorf("Backoff(%d) = %v, shrank from %v", i, got, prev)
		}
		if got > 30*time.Minute {
			t.Errorf("Backoff(%d) = %v, exceeds the 30m cap", i, got)
		}
		prev = got
	}
	if ghsync.Backoff(1) != time.Minute {
		t.Errorf("Backoff(1) = %v, want 1m", ghsync.Backoff(1))
	}
}

// TestBackoffClampsExtremeAttempts exercises the edges Backoff guards against:
// an attempts value below 1 (not meaningful — the first attempt is 1), and an
// attempts value far larger than MaxAttempts could ever reach, which must
// still land on the 30m cap rather than overflowing.
func TestBackoffClampsExtremeAttempts(t *testing.T) {
	tests := []struct {
		name     string
		attempts int
		want     time.Duration
	}{
		{name: "zero attempts clamps to the first backoff", attempts: 0, want: time.Minute},
		{name: "negative attempts clamps to the first backoff", attempts: -5, want: time.Minute},
		{name: "far beyond MaxAttempts stays capped", attempts: 1000, want: 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ghsync.Backoff(tt.attempts); got != tt.want {
				t.Errorf("Backoff(%d) = %v, want %v", tt.attempts, got, tt.want)
			}
		})
	}
}

// TestDrainPullRequest exercises the KindPR happy path: the reference brief's
// test file (Task 6) never exercises pull-request delivery, so this fills
// that gap — CreatePullRequest is called and the resulting number/URL are
// persisted back onto the ticket.
func TestDrainPullRequest(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "plan")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindPR,
		Payload:        `{"head":"ticket/add-rate-limiting-t1","base":"main","title":"Add rate limiting","body":"Ready for review."}`,
		IdempotencyKey: ghsync.PRKey("t1"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	s := ghsync.NewSyncer(gdb, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	prs := f.PRsSnapshot()
	if len(prs) != 1 {
		t.Fatalf("PRs created = %d, want 1", len(prs))
	}
	var ticket db.Ticket
	if err := gdb.First(&ticket, "id = ?", "t1").Error; err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	if ticket.PRNumber == nil || *ticket.PRNumber != prs[0].Number {
		t.Errorf("ticket.PRNumber = %v, want %d", ticket.PRNumber, prs[0].Number)
	}
	if ticket.PRURL != prs[0].HTMLURL {
		t.Errorf("ticket.PRURL = %q, want %q", ticket.PRURL, prs[0].HTMLURL)
	}

	var row db.GitHubOutbox
	gdb.First(&row, "ticket_id = ?", "t1")
	if row.DoneAt == nil {
		t.Error("DoneAt nil after successful PR drain")
	}
}

// TestDrainRecordsLinkageFailuresWithoutAbortingBatch covers the three ways a
// row can fail before ever reaching GitHub: a ticket with no linked issue, a
// ticket whose repo was never registered, and an outbox row pointing at a
// ticket that no longer exists. In every case Drain itself must still return
// nil — one bad row must not block the rest of the queue — while the row is
// left retryable with a descriptive LastError.
func TestDrainRecordsLinkageFailuresWithoutAbortingBatch(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, gdb *gorm.DB)
		ticketID  string
		wantErrIn string
	}{
		{
			name: "ticket has no linked issue",
			setup: func(t *testing.T, gdb *gorm.DB) {
				newRepo(t, gdb)
				tk := db.Ticket{ID: "no-issue", RepoRemote: "https://github.com/org/repo",
					Title: "x", Branch: "b", BaseBranch: "main", Description: "d", Phase: "plan"}
				if err := gdb.Create(&tk).Error; err != nil {
					t.Fatalf("seed ticket: %v", err)
				}
			},
			ticketID:  "no-issue",
			wantErrIn: "has no linked issue",
		},
		{
			name: "ticket's repo was never registered",
			setup: func(t *testing.T, gdb *gorm.DB) {
				tk := db.Ticket{ID: "orphan-repo", RepoRemote: "https://github.com/org/unregistered",
					Title: "x", Branch: "b", BaseBranch: "main", Description: "d", Phase: "plan",
					IssueNumber: intPtr(1)}
				if err := gdb.Create(&tk).Error; err != nil {
					t.Fatalf("seed ticket: %v", err)
				}
			},
			ticketID:  "orphan-repo",
			wantErrIn: "load repo",
		},
		{
			name:      "ticket does not exist",
			setup:     func(t *testing.T, gdb *gorm.DB) {},
			ticketID:  "does-not-exist",
			wantErrIn: "load ticket",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, _ := db.Open(":memory:")
			tt.setup(t, gdb)

			if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
				TicketID: tt.ticketID, Kind: ghsync.KindComment, Payload: `{"body":"x"}`,
				IdempotencyKey: ghsync.CommentKey(tt.ticketID, "x"),
			}); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			s := ghsync.NewSyncer(gdb, github.NewFake())
			if err := s.Drain(context.Background()); err != nil {
				t.Fatalf("Drain: %v", err)
			}

			var row db.GitHubOutbox
			gdb.First(&row, "ticket_id = ?", tt.ticketID)
			if row.DoneAt != nil {
				t.Error("DoneAt set despite delivery failure")
			}
			if row.Attempts != 1 {
				t.Errorf("Attempts = %d, want 1", row.Attempts)
			}
			if !strings.Contains(row.LastError, tt.wantErrIn) {
				t.Errorf("LastError = %q, want substring %q", row.LastError, tt.wantErrIn)
			}
		})
	}
}

// TestDrainSurfacesPayloadAndKindErrors covers a malformed JSON payload for
// each outbox kind that decodes one, plus a row carrying a kind the drain
// loop does not recognise. All are decode/dispatch errors that never reach
// GitHub, and Drain must record them as an ordinary retryable failure rather
// than returning an error itself.
func TestDrainSurfacesPayloadAndKindErrors(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		payload   string
		wantErrIn string
	}{
		{name: "malformed comment payload", kind: ghsync.KindComment, payload: `{bad`, wantErrIn: "decode comment payload"},
		{name: "malformed label payload", kind: ghsync.KindLabel, payload: `{bad`, wantErrIn: "decode label payload"},
		{name: "malformed pr payload", kind: ghsync.KindPR, payload: `{bad`, wantErrIn: "decode pr payload"},
		{name: "unrecognised kind", kind: "bogus", payload: `{}`, wantErrIn: "unknown outbox kind"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, _ := db.Open(":memory:")
			seedLinkedTicket(t, gdb, "plan")
			f := github.NewFake()
			f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

			if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
				TicketID: "t1", Kind: tt.kind, Payload: tt.payload,
				IdempotencyKey: ghsync.CommentKey("t1", tt.name+string(rune('0'+i))),
			}); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			s := ghsync.NewSyncer(gdb, f)
			if err := s.Drain(context.Background()); err != nil {
				t.Fatalf("Drain: %v", err)
			}

			var row db.GitHubOutbox
			gdb.First(&row, "ticket_id = ?", "t1")
			if row.Attempts != 1 {
				t.Errorf("Attempts = %d, want 1", row.Attempts)
			}
			if !strings.Contains(row.LastError, tt.wantErrIn) {
				t.Errorf("LastError = %q, want substring %q", row.LastError, tt.wantErrIn)
			}
		})
	}
}

// TestDrainRetriesOnGitHubFailureAcrossKinds drives a GitHub-side failure
// (via the fake's FailNext lever) through the three kinds where the first
// GitHub call in deliver can fail: close, label (GetIssue is the first call
// applyPhaseLabel makes), and pull-request creation. Each must leave the row
// retryable rather than done, and must not leave a partial side effect (no PR
// recorded) behind.
func TestDrainRetriesOnGitHubFailureAcrossKinds(t *testing.T) {
	tests := []struct {
		name    string
		phase   string
		kind    string
		payload string
	}{
		{name: "close fails", phase: "closed", kind: ghsync.KindClose, payload: `{}`},
		{name: "label fails on GetIssue", phase: "implement", kind: ghsync.KindLabel, payload: `{"phase":"implement"}`},
		{name: "pr creation fails", phase: "plan", kind: ghsync.KindPR,
			payload: `{"head":"h","base":"main","title":"t","body":"b"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, _ := db.Open(":memory:")
			seedLinkedTicket(t, gdb, tt.phase)
			f := github.NewFake()
			f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})
			f.SetFailNext(errors.New("boom"))

			if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
				TicketID: "t1", Kind: tt.kind, Payload: tt.payload,
				IdempotencyKey: ghsync.CommentKey("t1", tt.name),
			}); err != nil {
				t.Fatalf("Enqueue: %v", err)
			}
			s := ghsync.NewSyncer(gdb, f)
			if err := s.Drain(context.Background()); err != nil {
				t.Fatalf("Drain: %v", err)
			}

			var row db.GitHubOutbox
			gdb.First(&row, "ticket_id = ?", "t1")
			if row.DoneAt != nil {
				t.Errorf("%s: DoneAt set despite GitHub failure", tt.name)
			}
			if row.Attempts != 1 {
				t.Errorf("%s: Attempts = %d, want 1", tt.name, row.Attempts)
			}
			if len(f.PRsSnapshot()) != 0 {
				t.Errorf("%s: PR created despite CreatePullRequest failing", tt.name)
			}
		})
	}
}

// TestDrainWrapsQueryError confirms Drain's own row-selection query failure
// is wrapped and returned (rather than swallowed), by dropping the outbox
// table out from under it.
func TestDrainWrapsQueryError(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	if err := gdb.Exec("DROP TABLE git_hub_outboxes").Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	s := ghsync.NewSyncer(gdb, github.NewFake())
	err := s.Drain(context.Background())
	if err == nil {
		t.Fatal("Drain: want error after outbox table dropped, got nil")
	}
	if !strings.Contains(err.Error(), "query outbox rows") {
		t.Errorf("Drain error = %q, want it to mention %q", err.Error(), "query outbox rows")
	}
}

// TestDrainCommitsPostWriteAndDoneAtAtomically forces the done_at write that
// finalises a successful KindPR delivery to fail, via a SQLite trigger that
// aborts any UPDATE setting done_at to non-null (mirroring the trigger
// technique already used in ingest_test.go's partial-failure coverage). This
// simulates a crash — or any other local-write failure — landing between
// GitHub accepting the CreatePullRequest call and Golem committing that fact
// locally.
//
// It asserts the two things the fix round asked for: the ticket's
// pr_number/pr_url — written in the same transaction as done_at — were
// rolled back with it rather than left half-applied, and the row is left
// retryable (attempts incremented, LastError set, done_at still nil) rather
// than either silently "done" (which would permanently lose the PR link) or
// silently stuck with no record of the failure.
func TestDrainCommitsPostWriteAndDoneAtAtomically(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "plan")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

	// Fires only for an UPDATE that sets done_at to a non-null value, so
	// recordFailure's own update (attempts/last_error/next_attempt, never
	// done_at) is untouched by this trigger and continues to work normally.
	if err := gdb.Exec(`
		CREATE TRIGGER outbox_done_write_fails
		BEFORE UPDATE OF done_at ON git_hub_outboxes
		WHEN NEW.done_at IS NOT NULL
		BEGIN
			SELECT RAISE(ABORT, 'simulated done_at write failure');
		END;
	`).Error; err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindPR,
		Payload:        `{"head":"ticket/add-rate-limiting-t1","base":"main","title":"Add rate limiting","body":"Ready for review."}`,
		IdempotencyKey: ghsync.PRKey("t1"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	s := ghsync.NewSyncer(gdb, f)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// GitHub's side already happened — the fake recorded the PR — but the
	// local write recording it on the ticket must have rolled back along
	// with the done_at write it shares a transaction with.
	if prs := f.PRsSnapshot(); len(prs) != 1 {
		t.Fatalf("PRs created = %d, want 1 (the GitHub call itself must still go through)", len(prs))
	}
	var ticket db.Ticket
	if err := gdb.First(&ticket, "id = ?", "t1").Error; err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	if ticket.PRNumber != nil {
		t.Errorf("ticket.PRNumber = %v, want nil — the rolled-back transaction must not leave it written", *ticket.PRNumber)
	}
	if ticket.PRURL != "" {
		t.Errorf("ticket.PRURL = %q, want empty — the rolled-back transaction must not leave it written", ticket.PRURL)
	}

	var row db.GitHubOutbox
	gdb.First(&row, "ticket_id = ?", "t1")
	if row.DoneAt != nil {
		t.Error("DoneAt set despite the commit transaction failing")
	}
	if row.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 — a failed commit must be retried like any other delivery failure", row.Attempts)
	}
	if row.LastError == "" {
		t.Error("LastError empty after the commit transaction failed")
	}
}
