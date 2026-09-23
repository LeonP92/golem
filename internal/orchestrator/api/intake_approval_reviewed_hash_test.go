package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// startRequest posts the start action, optionally carrying the body hash the
// operator's page was rendered from.
func startRequest(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, ticketID, reviewedHash string) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]string{"action": "start"}
	if reviewedHash != "" {
		payload["reviewed_body_hash"] = reviewedHash
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticketID+"/actions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// pendingTicketIssueNumber hands out a distinct issue number per seeded
// ticket, since (repo_remote, issue_number) is unique.
var pendingTicketIssueNumber atomic.Int64

// seedPendingTicket creates a GitHub-linked ticket awaiting intake approval,
// with body_hash consistent with its description the way ingest leaves it.
func seedPendingTicket(t *testing.T, h *api.Handlers, description string) db.Ticket {
	t.Helper()
	number := int(pendingTicketIssueNumber.Add(1))
	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: description, BodyHash: ghsync.HashDescription(description),
		Phase: "pending-approval", IssueNumber: &number,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	return ticket
}

// ingestCommits stands in for a ghsync poll landing new issue text: it writes
// exactly the columns applyIssue writes for a body change on an unapproved
// ticket.
func ingestCommits(t *testing.T, h *api.Handlers, ticketID, newBody string) {
	t.Helper()
	if err := h.DB.Model(&db.Ticket{}).Where("id = ?", ticketID).
		Updates(map[string]any{
			"description": newBody,
			"body_hash":   ghsync.HashDescription(newBody),
		}).Error; err != nil {
		t.Fatalf("simulate ingest: %v", err)
	}
}

// TestStartRefusesTextTheOperatorNeverSaw is fix round 1c's reason to exist.
//
// Round 1b moved actionStart's hash off a pre-transaction snapshot and onto a
// row reloaded inside the transaction, which fixed a ticket being stranded
// unclaimable (finding I5). But it also changed what happens when an ingest
// pass commits new issue text in the window between the operator's page being
// RENDERED and their clicking Approve: the old code bound the approval to the
// text it read at POST time and failed closed; the new code binds it to the
// text in its own transaction, which by then is the edited text. The operator
// approves — and a shem then receives — text they never read. Fail-open where
// the bug it replaced was fail-closed, on the one property this branch exists
// to guarantee.
//
// The window is real and wide: it is however long a human spends reading the
// description. It cannot be closed inside actionStart, because nothing in the
// request tells the server what the operator was shown. The page now renders
// the ticket's body_hash, the approval submits it, and actionStart requires it
// to still match inside its transaction.
//
// Verified against HEAD 1e7f0d9 before the fix: 204, intake_approved=true,
// approved_body_hash == body_hash == H("edited by a stranger"), and the
// ticket claimable — serving the edited text.
func TestStartRefusesTextTheOperatorNeverSaw(t *testing.T) {
	h, mux, cookie := setupActionTest(t)
	h.RegisterTicketRoutes(mux)
	shem := seedShem(t, h, "reviewed-shem", "reviewedkey")

	const original = "the description a human read"
	const edited = "edited by a stranger"
	ticket := seedPendingTicket(t, h, original)

	// The operator loads the ticket page. This is the hash it renders into
	// the approval control.
	var shown db.Ticket
	if err := h.DB.First(&shown, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("load ticket as the page would: %v", err)
	}
	reviewed := shown.BodyHash

	// While they read it, a poll lands the issue author's edit.
	ingestCommits(t, h, ticket.ID, edited)

	// They click Approve.
	w := startRequest(t, mux, cookie, ticket.ID, reviewed)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — the approval must not certify text the operator never saw: %s",
			w.Code, w.Body.String())
	}
	if msg := w.Body.String(); !strings.Contains(strings.ToLower(msg), "reload") {
		t.Errorf("409 body = %q, want it to tell the operator to reload and re-read", strings.TrimSpace(msg))
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.IntakeApproved {
		t.Error("intake_approved set despite the refusal")
	}
	if got.Phase != "pending-approval" {
		t.Errorf("phase = %q, want pending-approval — the refusal must roll the whole transaction back", got.Phase)
	}
	if got.ApprovedBodyHash != "" {
		t.Errorf("approved_body_hash = %q, want empty", got.ApprovedBodyHash)
	}
	if _, err := h.ClaimTicket(ticket.ID, shem.ID); err == nil {
		t.Error("ticket is claimable after a refused approval")
	}
	// body_hash is ghsync's alone; a refused approval must not have touched it.
	if got.BodyHash != ghsync.HashDescription(edited) {
		t.Errorf("body_hash = %q, want the ingest-written hash of the edited text", got.BodyHash)
	}
	var labels int64
	h.DB.Model(&db.GitHubOutbox{}).Where("ticket_id = ?", ticket.ID).Count(&labels)
	if labels != 0 {
		t.Errorf("outbox rows = %d, want 0 — a refused approval must queue nothing", labels)
	}

	// Recovery is a reload: the operator re-reads the new text and approves
	// that. No GitHub edit, no database surgery.
	if w := startRequest(t, mux, cookie, ticket.ID, ghsync.HashDescription(edited)); w.Code != http.StatusNoContent {
		t.Fatalf("re-approval after reload = %d, want 204: %s", w.Code, w.Body.String())
	}
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if !got.IntakeApproved || got.Phase != "unassigned" {
		t.Errorf("after re-approval: intake_approved=%v phase=%q, want true/unassigned",
			got.IntakeApproved, got.Phase)
	}
	if got.ApprovedBodyHash != got.BodyHash {
		t.Errorf("approved_body_hash = %q, body_hash = %q — want them equal", got.ApprovedBodyHash, got.BodyHash)
	}
	if _, err := h.ClaimTicket(ticket.ID, shem.ID); err != nil {
		t.Errorf("claim after re-approval: %v", err)
	}
}

// TestStartWithoutInterleavingApprovesInOneClick is the control for the test
// above: the ordinary path — nothing changes while the operator reads — must
// still be a single click, with no extra step and no second request.
func TestStartWithoutInterleavingApprovesInOneClick(t *testing.T) {
	h, mux, cookie := setupActionTest(t)
	h.RegisterTicketRoutes(mux)
	shem := seedShem(t, h, "plain-shem", "plainkey")

	ticket := seedPendingTicket(t, h, "an ordinary description")

	var shown db.Ticket
	if err := h.DB.First(&shown, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("load ticket as the page would: %v", err)
	}

	if w := startRequest(t, mux, cookie, ticket.ID, shown.BodyHash); w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if !got.IntakeApproved || got.Phase != "unassigned" {
		t.Errorf("intake_approved=%v phase=%q, want true/unassigned", got.IntakeApproved, got.Phase)
	}
	if got.ApprovedBodyHash != got.BodyHash {
		t.Errorf("approved_body_hash = %q, body_hash = %q — want them equal", got.ApprovedBodyHash, got.BodyHash)
	}
	if _, err := h.ClaimTicket(ticket.ID, shem.ID); err != nil {
		t.Errorf("claim after approval: %v", err)
	}
}

// TestStartRejectsAMissingOrWrongReviewedHash covers the rest of the contract.
// A start with no reviewed hash at all is the shape a page cached from before
// this change produces, and is also the shape any caller would use to opt out
// of the check — so it must be refused rather than treated as "no opinion".
func TestStartRejectsAMissingOrWrongReviewedHash(t *testing.T) {
	cases := []struct {
		name     string
		hashFor  func(current string) string
		wantCode int
	}{
		{
			name:     "no reviewed hash at all",
			hashFor:  func(string) string { return "" },
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "a hash of some other text",
			hashFor:  func(string) string { return ghsync.HashDescription("text that was never on this ticket") },
			wantCode: http.StatusConflict,
		},
		{
			name:     "a non-hash string",
			hashFor:  func(string) string { return "not-a-hash" },
			wantCode: http.StatusConflict,
		},
		{
			name:     "the current hash",
			hashFor:  func(current string) string { return current },
			wantCode: http.StatusNoContent,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)
			ticket := seedPendingTicket(t, h, "a description")

			w := startRequest(t, mux, cookie, ticket.ID, tc.hashFor(ticket.BodyHash))
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}

			var got db.Ticket
			if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
				t.Fatalf("reload ticket: %v", err)
			}
			wantApproved := tc.wantCode == http.StatusNoContent
			if got.IntakeApproved != wantApproved {
				t.Errorf("intake_approved = %v, want %v", got.IntakeApproved, wantApproved)
			}
		})
	}
}

// TestStartAcceptsTheReviewedHashFromAFormBody covers the other content type
// the dispatcher accepts. htmx serialises hx-vals into an urlencoded body for
// a POST, so this is the shape the dashboard's own control actually sends —
// and it must keep coming from the BODY, never the query string (finding S1).
func TestStartAcceptsTheReviewedHashFromAFormBody(t *testing.T) {
	h, mux, cookie := setupActionTest(t)
	ticket := seedPendingTicket(t, h, "a description")

	form := "action=start&reviewed_body_hash=" + ticket.BodyHash
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions",
		strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
	}

	// The same hash in the query string of an otherwise bodyless POST must
	// not work: that is the injected-form shape round 1a closed.
	other := seedPendingTicket(t, h, "another description")
	req = httptest.NewRequest(http.MethodPost,
		"/api/tickets/"+other.ID+"/actions?action=start&reviewed_body_hash="+other.BodyHash, nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusNoContent {
		t.Error("a start supplied entirely in the query string was accepted")
	}
	var got db.Ticket
	h.DB.First(&got, "id = ?", other.ID)
	if got.IntakeApproved {
		t.Error("query-string start approved the ticket")
	}
}
