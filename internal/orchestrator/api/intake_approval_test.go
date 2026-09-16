package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// doTicketAction posts a human action to /api/tickets/{id}/actions with
// session auth and returns the response recorder.
func doTicketAction(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, ticketID, action string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": action})
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/tickets/%s/actions", ticketID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestPendingApprovalTicketNeverBecomesClaimableThroughPhaseChain is the
// fix-round-3 property test for the intake approval gate (spec Amendment 1).
// Rounds 1 and 2 guarded individual phase-transition doors (requeue,
// needs-attention) one at a time; the reviewer then found a third hole of
// identical shape laundered through TWO intermediate phases:
//
//	pending-approval -> close -> needs-attention -> requeue -> unassigned
//
// three authenticated POSTs to the same endpoint, ending in a claimable
// ticket whose untrusted issue body was never reviewed — and along the way
// GitHub is told the issue is closed while the ticket is actually claimable.
//
// The fix makes approval a persisted, one-way fact (db.Ticket.IntakeApproved,
// written only by actionStart) checked at the two claim points, instead of
// something inferred from the current phase. This test asserts the SECURITY
// PROPERTY directly — claimability via the real HTTP endpoints — not the
// phase string, which legitimately ends at "unassigned".
func TestPendingApprovalTicketNeverBecomesClaimableThroughPhaseChain(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "chain-shem", "chainkey")
	_, cookie := seedSessionUser(t, h.DB, "chain-admin")

	n := 99
	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/chain",
		Description: "untrusted issue body",
		Phase:       "pending-approval",
		IssueNumber: &n,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	// Walk the laundering chain. Each step is expected to SUCCEED (204) —
	// none of these individual transitions is illegitimate on its own; the
	// point of this test is that the end state must still not be claimable.
	steps := []string{"close", "needs-attention", "requeue"}
	for _, action := range steps {
		w := doTicketAction(t, mux, cookie, ticket.ID, action)
		if w.Code != http.StatusNoContent {
			t.Fatalf("action=%q: expected 204, got %d: %s", action, w.Code, w.Body.String())
		}
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.Phase != "unassigned" {
		t.Fatalf("phase = %q, want unassigned (this is expected and fine — the "+
			"security property below is what must hold)", got.Phase)
	}
	if got.IntakeApproved {
		t.Fatal("intake_approved = true, want false — actionStart is the only " +
			"handler allowed to set it, and it was never called")
	}

	// The property: absent from the available list.
	availReq := httptest.NewRequest(http.MethodGet,
		"/api/tickets/available?repo=https://github.com/org/repo", nil)
	availReq.Header.Set("Authorization", "Bearer chainkey")
	availReq.Header.Set("X-Shem-Name", "chain-shem")
	availW := httptest.NewRecorder()
	mux.ServeHTTP(availW, availReq)
	if availW.Code != http.StatusOK {
		t.Fatalf("available: expected 200, got %d: %s", availW.Code, availW.Body.String())
	}
	var available []db.Ticket
	if err := json.Unmarshal(availW.Body.Bytes(), &available); err != nil {
		t.Fatalf("decode available: %v", err)
	}
	for _, at := range available {
		if at.ID == ticket.ID {
			t.Fatal("laundered ticket appeared in /api/tickets/available — gate bypassed")
		}
	}

	// The property: claim is rejected.
	claimReq := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/claim", nil)
	claimReq.Header.Set("Authorization", "Bearer chainkey")
	claimReq.Header.Set("X-Shem-Name", "chain-shem")
	claimW := httptest.NewRecorder()
	mux.ServeHTTP(claimW, claimReq)
	if claimW.Code != http.StatusConflict {
		t.Fatalf("claim: expected 409, got %d: %s — laundered pending-approval "+
			"ticket was claimable", claimW.Code, claimW.Body.String())
	}

	// Confirm the claim attempt truly did not assign the ticket (belt and
	// braces beyond the status code).
	var afterClaim db.Ticket
	if err := h.DB.First(&afterClaim, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket after claim attempt: %v", err)
	}
	if afterClaim.AssignedShem != nil {
		t.Errorf("assigned_shem = %v after a rejected claim, want nil", *afterClaim.AssignedShem)
	}
	if afterClaim.Phase != "unassigned" {
		t.Errorf("phase = %q after a rejected claim attempt, want unchanged unassigned", afterClaim.Phase)
	}
}

// TestWebFormTicketStillClaimableImmediately guards against the
// intake_approved predicate regressing into blocking everything: a ticket
// created through the orchestrator's own web form has a nil IssueNumber and
// was never subject to the intake gate at all, so it must be claimable the
// moment it is unassigned — with no start action, no IntakeApproved flag set.
func TestWebFormTicketStillClaimableImmediately(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "webform-shem", "webformkey")

	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/webform",
		Description: "written by an authenticated human via the dashboard form",
		Phase:       "unassigned",
		// IssueNumber intentionally left nil: this is what createTicket and
		// ticketNewSubmit both produce. IntakeApproved is left at its zero
		// value (false) — exactly as those handlers leave it.
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	availReq := httptest.NewRequest(http.MethodGet,
		"/api/tickets/available?repo=https://github.com/org/repo", nil)
	availReq.Header.Set("Authorization", "Bearer webformkey")
	availReq.Header.Set("X-Shem-Name", "webform-shem")
	availW := httptest.NewRecorder()
	mux.ServeHTTP(availW, availReq)
	if availW.Code != http.StatusOK {
		t.Fatalf("available: expected 200, got %d: %s", availW.Code, availW.Body.String())
	}
	var available []db.Ticket
	if err := json.Unmarshal(availW.Body.Bytes(), &available); err != nil {
		t.Fatalf("decode available: %v", err)
	}
	found := false
	for _, at := range available {
		if at.ID == ticket.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("web-form ticket (nil issue_number, intake_approved=false) did not " +
			"appear in /api/tickets/available — the intake_approved predicate " +
			"regressed into blocking tickets it was never meant to gate")
	}

	claimReq := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/claim", nil)
	claimReq.Header.Set("Authorization", "Bearer webformkey")
	claimReq.Header.Set("X-Shem-Name", "webform-shem")
	claimW := httptest.NewRecorder()
	mux.ServeHTTP(claimW, claimReq)
	if claimW.Code != http.StatusOK {
		t.Fatalf("claim: expected 200, got %d: %s — web-form ticket should be "+
			"claimable immediately", claimW.Code, claimW.Body.String())
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.Phase != "claimed" {
		t.Errorf("phase = %q, want claimed", got.Phase)
	}
}

// TestStartRecoversTicketStrandedOutsidePendingApproval is the fix-round-4
// availability test: before this round, a GitHub-sourced ticket that left
// pending-approval by any route other than start (e.g. a single misclick of
// the dashboard's own needs-attention or close control) was permanently
// unclaimable with no in-product recovery, since re-ingesting the same
// issue is blocked by the unique (repo_remote, issue_number) index.
// Widening actionStart's guard to be provenance-based (issue_number set,
// intake_approved false, not closed) instead of phase-based makes this
// recoverable: pending-approval -> needs-attention (no start in between) ->
// start must still succeed and the ticket must become genuinely claimable
// through the real endpoints.
func TestStartRecoversTicketStrandedOutsidePendingApproval(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "recover-shem", "recoverkey")
	_, cookie := seedSessionUser(t, h.DB, "recover-admin")

	n := 77
	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/recover",
		Description: "d",
		// BodyHash mirrors what createTicketFromIssue always sets for a
		// real GitHub-linked ticket; this test constructs the row directly
		// (bypassing ghsync), so it must uphold that invariant itself, or
		// the round-5 claim predicate's approved_body_hash = body_hash
		// check would never match even after a correct approval.
		BodyHash:    ghsync.HashBody("d"),
		Phase:       "pending-approval",
		IssueNumber: &n,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	// Strand it, with no start in between: needs-attention itself refuses a
	// direct pending-approval -> needs-attention transition (round 2's
	// guard), so reaching needs-attention with intake_approved still false
	// requires going via close first — the same "close, then needs-attention"
	// prefix of round 3's laundering chain, stopping short of requeue since
	// this test's point is recovery via start, not via requeue.
	if w := doTicketAction(t, mux, cookie, ticket.ID, "close"); w.Code != http.StatusNoContent {
		t.Fatalf("close: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if w := doTicketAction(t, mux, cookie, ticket.ID, "needs-attention"); w.Code != http.StatusNoContent {
		t.Fatalf("needs-attention: expected 204, got %d: %s", w.Code, w.Body.String())
	}
	var stranded db.Ticket
	h.DB.First(&stranded, "id = ?", ticket.ID)
	if stranded.Phase != "needs-attention" || stranded.IntakeApproved {
		t.Fatalf("setup: phase=%q intake_approved=%v, want needs-attention/false",
			stranded.Phase, stranded.IntakeApproved)
	}

	if w := doTicketAction(t, mux, cookie, ticket.ID, "start"); w.Code != http.StatusNoContent {
		t.Fatalf("start: expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var recovered db.Ticket
	if err := h.DB.First(&recovered, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if recovered.Phase != "unassigned" || !recovered.IntakeApproved {
		t.Fatalf("phase=%q intake_approved=%v, want unassigned/true after start",
			recovered.Phase, recovered.IntakeApproved)
	}

	// The property that matters: genuinely claimable through the real
	// endpoints, not just a phase string.
	availReq := httptest.NewRequest(http.MethodGet,
		"/api/tickets/available?repo=https://github.com/org/repo", nil)
	availReq.Header.Set("Authorization", "Bearer recoverkey")
	availReq.Header.Set("X-Shem-Name", "recover-shem")
	availW := httptest.NewRecorder()
	mux.ServeHTTP(availW, availReq)
	var available []db.Ticket
	if err := json.Unmarshal(availW.Body.Bytes(), &available); err != nil {
		t.Fatalf("decode available: %v", err)
	}
	found := false
	for _, at := range available {
		if at.ID == ticket.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("recovered ticket did not appear in /api/tickets/available")
	}

	claimReq := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/claim", nil)
	claimReq.Header.Set("Authorization", "Bearer recoverkey")
	claimReq.Header.Set("X-Shem-Name", "recover-shem")
	claimW := httptest.NewRecorder()
	mux.ServeHTTP(claimW, claimReq)
	if claimW.Code != http.StatusOK {
		t.Fatalf("claim: expected 200, got %d: %s", claimW.Code, claimW.Body.String())
	}
}
