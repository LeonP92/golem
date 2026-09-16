package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

// setupPRTest extends setupTicketTest with the GitHub routes, since
// branch-pushed lives there and the shared ticket test setup does not
// register them.
func setupPRTest(t *testing.T) (*api.Handlers, *http.ServeMux) {
	t.Helper()
	h, mux := setupTicketTest(t)
	h.RegisterGitHubRoutes(mux)
	return h, mux
}

// prTestRequest builds an API-key-authenticated request for the given shem.
func prTestRequest(t *testing.T, method, url, shemName, apiKey string, body []byte) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, reader)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("X-Shem-Name", shemName)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func doPhaseUpdateTo(t *testing.T, mux *http.ServeMux, ticketID, shemName, apiKey, phase string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"phase": phase})
	req := prTestRequest(t, http.MethodPatch, "/api/tickets/"+ticketID+"/phase", shemName, apiKey, body)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code
}

func doBranchPushed(t *testing.T, mux *http.ServeMux, ticketID, shemName, apiKey string) int {
	t.Helper()
	req := prTestRequest(t, http.MethodPost, "/api/tickets/"+ticketID+"/branch-pushed", shemName, apiKey, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code
}

// TestPRQueuedWhenBothConditionsHold pins the core property of Task 13: the
// PR is enqueued exactly once regardless of whether the phase transition or
// the branch-pushed callback happens first, because both paths share
// ghsync.PRKey(ticketID) as their idempotency key.
func TestPRQueuedWhenBothConditionsHold(t *testing.T) {
	tests := []struct {
		name    string
		order   string // "phase-first" or "push-first"
		wantPRs int64
	}{
		{name: "phase then push", order: "phase-first", wantPRs: 1},
		{name: "push then phase", order: "push-first", wantPRs: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mux := setupPRTest(t)
			shem := seedShem(t, h, "pr-shem", "prkey")

			n := 7
			ticket := db.Ticket{
				RepoRemote:   "https://github.com/org/repo",
				Title:        "Add rate limiting",
				Branch:       "ticket/add-rate-limiting-t1",
				BaseBranch:   "main",
				Description:  "d",
				Phase:        "implement",
				AssignedShem: &shem.ID,
				IssueNumber:  &n,
			}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			phase := func() {
				if code := doPhaseUpdateTo(t, mux, ticket.ID, "pr-shem", "prkey", "ready-for-review"); code != http.StatusNoContent {
					t.Fatalf("phase status = %d", code)
				}
			}
			push := func() {
				if code := doBranchPushed(t, mux, ticket.ID, "pr-shem", "prkey"); code != http.StatusNoContent {
					t.Fatalf("push status = %d", code)
				}
			}

			if tt.order == "phase-first" {
				phase()
				push()
			} else {
				push()
				phase()
			}

			var prs int64
			h.DB.Model(&db.GitHubOutbox{}).Where("kind = ?", ghsync.KindPR).Count(&prs)
			if prs != tt.wantPRs {
				t.Errorf("pr rows = %d, want %d", prs, tt.wantPRs)
			}

			var row db.GitHubOutbox
			if err := h.DB.First(&row, "kind = ?", ghsync.KindPR).Error; err != nil {
				t.Fatalf("load pr row: %v", err)
			}
			for _, want := range []string{"ticket/add-rate-limiting-t1", "main", "Closes #7"} {
				if !strings.Contains(row.Payload, want) {
					t.Errorf("pr payload missing %q: %s", want, row.Payload)
				}
			}
			if row.IdempotencyKey != ghsync.PRKey(ticket.ID) {
				t.Errorf("idempotency key = %q, want %q", row.IdempotencyKey, ghsync.PRKey(ticket.ID))
			}
		})
	}
}

// TestNoPRWhenBranchNeverPushed asserts that reaching ready-for-review alone
// (the no_push: true deployment path) never enqueues a PR.
func TestNoPRWhenBranchNeverPushed(t *testing.T) {
	h, mux := setupPRTest(t)
	shem := seedShem(t, h, "pr-shem-nopush", "prkeynopush")

	n := 7
	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Title:        "t",
		Branch:       "ticket/t-t1",
		BaseBranch:   "main",
		Description:  "d",
		Phase:        "implement",
		AssignedShem: &shem.ID,
		IssueNumber:  &n,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	if code := doPhaseUpdateTo(t, mux, ticket.ID, "pr-shem-nopush", "prkeynopush", "ready-for-review"); code != http.StatusNoContent {
		t.Fatalf("status = %d", code)
	}

	var prs int64
	h.DB.Model(&db.GitHubOutbox{}).Where("kind = ?", ghsync.KindPR).Count(&prs)
	if prs != 0 {
		t.Errorf("pr rows = %d with no branch pushed, want 0", prs)
	}
}

// TestNoPRForUnlinkedTicket asserts that a ticket with no linked GitHub issue
// (nil IssueNumber, the web-form creation path) never enqueues a PR even once
// both the phase and branch-pushed preconditions hold.
func TestNoPRForUnlinkedTicket(t *testing.T) {
	h, mux := setupPRTest(t)
	shem := seedShem(t, h, "pr-shem-unlinked", "prkeyunlinked")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Title:        "t",
		Branch:       "ticket/unlinked-t1",
		BaseBranch:   "main",
		Description:  "d",
		Phase:        "implement",
		AssignedShem: &shem.ID,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	if code := doBranchPushed(t, mux, ticket.ID, "pr-shem-unlinked", "prkeyunlinked"); code != http.StatusNoContent {
		t.Fatalf("push status = %d", code)
	}
	if code := doPhaseUpdateTo(t, mux, ticket.ID, "pr-shem-unlinked", "prkeyunlinked", "ready-for-review"); code != http.StatusNoContent {
		t.Fatalf("phase status = %d", code)
	}

	var prs int64
	h.DB.Model(&db.GitHubOutbox{}).Where("kind = ?", ghsync.KindPR).Count(&prs)
	if prs != 0 {
		t.Errorf("pr rows = %d for unlinked ticket, want 0", prs)
	}

	var updated db.Ticket
	if err := h.DB.First(&updated, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if !updated.BranchPushed {
		t.Error("branch_pushed not recorded for unlinked ticket")
	}
}

// TestBranchPushed_WrongShem_Conflict asserts that branch-pushed is guarded
// the same way updatePhase is: a shem that does not own the ticket gets 409
// and the ticket is left completely unchanged.
func TestBranchPushed_WrongShem_Conflict(t *testing.T) {
	h, mux := setupPRTest(t)
	owner := seedShem(t, h, "pr-owner", "ownerkey")
	seedShem(t, h, "pr-intruder", "intruderkey")

	n := 9
	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Title:        "t",
		Branch:       "ticket/wrong-shem-t1",
		BaseBranch:   "main",
		Description:  "d",
		Phase:        "ready-for-review",
		AssignedShem: &owner.ID,
		IssueNumber:  &n,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	code := doBranchPushed(t, mux, ticket.ID, "pr-intruder", "intruderkey")
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}

	var updated db.Ticket
	if err := h.DB.First(&updated, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if updated.BranchPushed {
		t.Error("branch_pushed set by a non-owning shem")
	}

	var prs int64
	h.DB.Model(&db.GitHubOutbox{}).Where("kind = ?", ghsync.KindPR).Count(&prs)
	if prs != 0 {
		t.Errorf("pr rows = %d after a rejected branch-pushed, want 0", prs)
	}
}

// TestBranchPushed_UnknownTicket_Conflict asserts that a branch-pushed call
// for a ticket ID that does not exist is rejected the same way an
// unowned-ticket call is (no row to match assigned_shem against), rather than
// panicking on a nil ticket lookup.
func TestBranchPushed_UnknownTicket_Conflict(t *testing.T) {
	h, mux := setupPRTest(t)
	seedShem(t, h, "pr-shem-missing", "prkeymissing")

	code := doBranchPushed(t, mux, "does-not-exist", "pr-shem-missing", "prkeymissing")
	if code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", code)
	}
}

// TestUpdatePhaseRace_ConcurrentBranchPushed_StillQueuesOnePR is fix round 1's
// regression test for a Critical the review found: updatePhase used to read
// the ticket via a plain, pre-transaction h.DB.First and hand that snapshot
// to enqueueGitHubPhase. If a branch-pushed call fully committed in the gap
// between that read and updatePhase's own transaction, the phase transition
// decided the PR precondition from a stale BranchPushed=false snapshot and
// declined — leaving the row with phase=ready-for-review AND
// branch_pushed=true (both preconditions genuinely true) but zero pr rows,
// permanently: reconcile.go never calls CreatePullRequest, so nothing ever
// recovers a PR lost this way. That is the inverse of the duplicate-PR
// failure the shared idempotency key guards against, and sequential-ordering
// tests (TestPRQueuedWhenBothConditionsHold) cannot see it, because nothing
// there ever hands enqueueGitHubPhase a snapshot that outlives a concurrent
// write.
//
// The interleaving is reproduced deterministically — not via goroutines or
// sleeps, which would make this flaky — by hooking GORM's query callback
// chain: the hook fires exactly once, immediately after the FIRST
// non-transactional read of this exact ticket by ID, and injects a full,
// committed POST .../branch-pushed call right there. That is precisely "a
// concurrent write commits in the gap between the pre-transaction read and
// the transaction that uses it." The hook deliberately never fires for a
// read taken *inside* an already-open transaction (guarded via the
// gorm.TxCommitter type assertion on Statement.ConnPool): no SQL backend
// lets a second writer commit against a row while a transaction already
// holds it, so injecting a competing write from inside one would simply
// deadlock (the outer transaction can never reach Commit while blocked
// inside its own callback, waiting on a lock it itself holds) — and, once
// the fix is applied, that in-transaction reload is the ONLY ticket read
// updatePhase performs, so the vulnerable, lock-free read this test targets
// no longer exists at all. When the hook never fires (fix in place), the
// test falls back to calling branch-pushed itself right after the phase
// update returns — ordinary sequential completion of both operations, which
// the fixed code already handles correctly (see TestPRQueuedWhenBothConditionsHold).
//
// Verified failing against the pre-fix code (this test was written and run
// before the fix below existed): prs = 0, "pr rows = 0, want exactly 1 once
// both preconditions are true" — reproducing the exact zero-PR defect the
// review reported, via the hook path (fired = true), not the fallback.
func TestUpdatePhaseRace_ConcurrentBranchPushed_StillQueuesOnePR(t *testing.T) {
	h, mux := setupPRTest(t)
	shem := seedShem(t, h, "race-shem", "racekey")

	n := 11
	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Title:        "Race regression",
		Branch:       "ticket/race-t1",
		BaseBranch:   "main",
		Description:  "d",
		Phase:        "implement",
		AssignedShem: &shem.ID,
		IssueNumber:  &n,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	var fired bool
	const hookName = "pr_race_test:inject_branch_pushed"
	if err := h.DB.Callback().Query().After("gorm:query").Register(hookName, func(tx *gorm.DB) {
		if fired {
			return
		}
		dest, ok := tx.Statement.Dest.(*db.Ticket)
		if !ok || dest.ID != ticket.ID {
			return
		}
		// A read taken inside an already-open transaction must never be
		// intercepted here: injecting a competing write from inside it
		// would deadlock against the very transaction we are nested in (no
		// backend allows a second writer to commit against a locked row,
		// and this transaction cannot reach Commit while blocked in its
		// own callback). Post-fix, updatePhase's only ticket read is
		// exactly such an in-transaction reload, so this guard is also
		// what makes the hook correctly never fire once the bug is fixed.
		if _, inTx := tx.Statement.ConnPool.(gorm.TxCommitter); inTx {
			return
		}
		fired = true
		if code := doBranchPushed(t, mux, ticket.ID, "race-shem", "racekey"); code != http.StatusNoContent {
			t.Fatalf("injected branch-pushed status = %d", code)
		}
	}); err != nil {
		t.Fatalf("register race hook: %v", err)
	}
	defer func() {
		if err := h.DB.Callback().Query().Remove(hookName); err != nil {
			t.Logf("remove race hook: %v", err)
		}
	}()

	if code := doPhaseUpdateTo(t, mux, ticket.ID, "race-shem", "racekey", "ready-for-review"); code != http.StatusNoContent {
		t.Fatalf("phase status = %d", code)
	}
	if !fired {
		// The vulnerable pre-transaction read is gone (the fix is in
		// place): perform the second operation now, exactly as a real
		// branch-pushed call arriving right after the phase transition
		// would.
		if code := doBranchPushed(t, mux, ticket.ID, "race-shem", "racekey"); code != http.StatusNoContent {
			t.Fatalf("push status = %d", code)
		}
	}
	// Disarm now, before the verification reads below: they also match a
	// *db.Ticket lookup by this ID, and left armed the hook would treat
	// the very first one as "the" targeted read and inject a third,
	// unwanted branch-pushed call.
	if err := h.DB.Callback().Query().Remove(hookName); err != nil {
		t.Fatalf("remove race hook: %v", err)
	}

	var final db.Ticket
	if err := h.DB.First(&final, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if final.Phase != "ready-for-review" || !final.BranchPushed {
		t.Fatalf("both preconditions must be true: phase=%q branch_pushed=%v", final.Phase, final.BranchPushed)
	}

	var prs int64
	h.DB.Model(&db.GitHubOutbox{}).Where("kind = ?", ghsync.KindPR).Count(&prs)
	if prs != 1 {
		t.Errorf("pr rows = %d, want exactly 1 once both preconditions are true", prs)
	}
}
