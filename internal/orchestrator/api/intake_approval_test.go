package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
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
