package ghsync_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// TestDrainPRRedeliveryConvergesOnTheExistingPullRequest covers finding I2.
// Outbox delivery is at-least-once: the GitHub call can never be inside the
// local transaction, so a failure between GitHub accepting CreatePullRequest
// and done_at landing redelivers the row. label and close_issue survive that
// (both are no-ops when already applied); KindPR did not.
//
// Against real GitHub the redelivered call answers
// "422 A pull request already exists for org:branch", which never succeeds:
// the PR exists, ticket.PRNumber stays nil forever, the row burns eight
// attempts and parks, and reconcile deliberately never creates pull
// requests, so nothing recovers it. One transient local write failure
// permanently orphaned a real pull request.
//
// Verified before the fix: PRs after retry = 2 against the old fake (which
// never refused), and against a fake that models the 422 the row simply
// failed again, ticket.PRNumber=<nil>.
func TestDrainPRRedeliveryConvergesOnTheExistingPullRequest(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "ready-for-review")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})

	// Fail the done_at write so the first delivery's local commit rolls
	// back after GitHub has already created the pull request.
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
		Payload:        `{"head":"ticket/add-rate-limiting-t1","base":"main","title":"Add rate limiting","body":"Closes #7"}`,
		IdempotencyKey: ghsync.PRKey("t1"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	s := ghsync.NewSyncer(gdb, f)
	ctx := context.Background()
	if err := s.Drain(ctx); err != nil {
		t.Fatalf("first Drain: %v", err)
	}
	if prs := f.PRsSnapshot(); len(prs) != 1 {
		t.Fatalf("PRs after first drain = %d, want 1", len(prs))
	}

	// The local failure is over; the row is redelivered.
	if err := gdb.Exec(`DROP TRIGGER outbox_done_write_fails`).Error; err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	// Make next_attempt due so Drain selects the row again.
	if err := gdb.Model(&db.GitHubOutbox{}).Where("ticket_id = ?", "t1").
		Update("next_attempt", "1970-01-01 00:00:00").Error; err != nil {
		t.Fatalf("reset next_attempt: %v", err)
	}
	if err := s.Drain(ctx); err != nil {
		t.Fatalf("retry Drain: %v", err)
	}

	prs := f.PRsSnapshot()
	if len(prs) != 1 {
		t.Fatalf("PRs after retry = %d, want 1 — the redelivery opened a second pull request", len(prs))
	}

	var ticket db.Ticket
	if err := gdb.First(&ticket, "id = ?", "t1").Error; err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	if ticket.PRNumber == nil || *ticket.PRNumber != prs[0].Number {
		t.Errorf("ticket.PRNumber = %v, want %d — the redelivery must record the pull request that exists",
			ticket.PRNumber, prs[0].Number)
	}
	if ticket.PRURL != prs[0].HTMLURL {
		t.Errorf("ticket.PRURL = %q, want %q", ticket.PRURL, prs[0].HTMLURL)
	}

	var row db.GitHubOutbox
	if err := gdb.First(&row, "ticket_id = ?", "t1").Error; err != nil {
		t.Fatalf("load outbox row: %v", err)
	}
	if row.DoneAt == nil {
		t.Errorf("outbox row still undone after the redelivery converged (attempts=%d, last_error=%q)",
			row.Attempts, row.LastError)
	}
}

// unprocessableGH answers every CreatePullRequest with a 422 and reports no
// existing pull request for any head — a 422 that does NOT mean "one already
// exists" (an invalid base branch, for instance).
type unprocessableGH struct {
	*github.Fake
	finds int
}

func (g *unprocessableGH) CreatePullRequest(_ context.Context, _, _, _, _, _, _ string, _ bool) (github.PullRequest, error) {
	return github.PullRequest{}, fmt.Errorf("%w: Validation Failed: base is invalid",
		github.ErrPullRequestUnprocessable)
}

func (g *unprocessableGH) FindPullRequest(_ context.Context, _, _, _ string) (github.PullRequest, bool, error) {
	g.finds++
	return github.PullRequest{}, false, nil
}

// TestDrainPRUnprocessableWithNoExistingPRStillFails is the negative control
// for the convergence above: a 422 must only be absorbed when the lookup
// actually finds the pull request it claims already exists. Any other 422
// has to keep taking the ordinary retry-and-park path, so the operator still
// learns the base branch was wrong instead of the row being marked done
// against a pull request that was never opened.
func TestDrainPRUnprocessableWithNoExistingPRStillFails(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "ready-for-review")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})
	gh := &unprocessableGH{Fake: f}

	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindPR,
		Payload:        `{"head":"ticket/add-rate-limiting-t1","base":"nonexistent","title":"t","body":"b"}`,
		IdempotencyKey: ghsync.PRKey("t1"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	s := ghsync.NewSyncer(gdb, gh)
	if err := s.Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	if gh.finds != 1 {
		t.Errorf("FindPullRequest calls = %d, want 1", gh.finds)
	}
	var row db.GitHubOutbox
	if err := gdb.First(&row, "ticket_id = ?", "t1").Error; err != nil {
		t.Fatalf("load outbox row: %v", err)
	}
	if row.DoneAt != nil {
		t.Error("row marked done despite no pull request existing")
	}
	if row.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", row.Attempts)
	}
	if !strings.Contains(row.LastError, "base is invalid") {
		t.Errorf("LastError = %q, want the original create failure", row.LastError)
	}

	var ticket db.Ticket
	gdb.First(&ticket, "id = ?", "t1")
	if ticket.PRNumber != nil {
		t.Errorf("ticket.PRNumber = %d, want nil", *ticket.PRNumber)
	}
}
