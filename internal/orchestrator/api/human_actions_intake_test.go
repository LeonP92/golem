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

// The dashboard actions that make up the intake approval gate (spec
// Amendment 1): "start", which is the only way a GitHub-sourced ticket
// becomes claimable, and the two actions that must refuse to release one by
// a side door. Split out of human_actions_test.go, which had grown past the
// 800-line maximum; the shared setup helpers stay there.
// TestStartActionReleasesPendingApprovalTicket verifies the intake approval
// gate release action (spec Amendment 1): "start" moves a pending-approval
// ticket to unassigned so a shem can claim it; any other phase must 409 and
// leave the ticket's phase untouched.
func TestStartActionReleasesPendingApprovalTicket(t *testing.T) {
	n := 42
	someShem := uint(99)
	tests := []struct {
		name           string
		phase          string
		issueNumber    *int
		intakeApproved bool
		assignedShem   *uint
		wantStatus     int
		wantPhase      string
	}{
		{name: "unapproved, linked, pending-approval -> released",
			phase: "pending-approval", issueNumber: &n, intakeApproved: false,
			wantStatus: http.StatusNoContent, wantPhase: "unassigned"},
		// Fix round 4: the guard is provenance-based (issue_number set,
		// intake_approved false, not closed), not phase-based, so this is
		// the recovery path for a ticket stranded outside pending-approval
		// by close/needs-attention/requeue — it must succeed, not 409, even
		// though the phase here is not "pending-approval".
		{name: "unapproved, linked, stranded in needs-attention -> recovered",
			phase: "needs-attention", issueNumber: &n, intakeApproved: false,
			wantStatus: http.StatusNoContent, wantPhase: "unassigned"},
		{name: "already approved and unassigned -> conflict, unchanged",
			phase: "unassigned", issueNumber: &n, intakeApproved: true,
			wantStatus: http.StatusConflict, wantPhase: "unassigned"},
		{name: "already approved mid-execution -> conflict, unchanged",
			phase: "implement", issueNumber: &n, intakeApproved: true,
			wantStatus: http.StatusConflict, wantPhase: "implement"},
		// Fix round 5 (the dangerous cell round 4's table left unpinned):
		// claimed AND unapproved must still 409, not succeed. Round 4's
		// guard checked only intake_approved, so this state — which should
		// never legitimately arise, but the guard did not defend against
		// it — was startable: 204, phase moved to unassigned with
		// assigned_shem still set, and a second shem could then claim the
		// same ticket. assigned_shem must also be unchanged afterward.
		{name: "claimed AND unapproved, mid-execution -> conflict, unchanged (double-claim guard)",
			phase: "implement", issueNumber: &n, intakeApproved: false, assignedShem: &someShem,
			wantStatus: http.StatusConflict, wantPhase: "implement"},
		{name: "not linked to a GitHub issue -> conflict, unchanged",
			phase: "unassigned", issueNumber: nil, intakeApproved: false,
			wantStatus: http.StatusConflict, wantPhase: "unassigned"},
		{name: "closed -> conflict, unchanged even though unapproved",
			phase: "closed", issueNumber: &n, intakeApproved: false,
			wantStatus: http.StatusConflict, wantPhase: "closed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)

			// BodyHash is what ingest writes alongside Description, and
			// what the page renders into the approval control (fix round
			// 1c). Every case below submits the matching hash, so each one
			// still exercises the guard it was written for rather than
			// stopping at the reviewed-hash check.
			ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d",
				BodyHash: ghsync.HashBody("d"),
				Phase:    tt.phase, IssueNumber: tt.issueNumber, IntakeApproved: tt.intakeApproved,
				AssignedShem: tt.assignedShem}
			h.DB.Create(&ticket)

			body, _ := json.Marshal(map[string]string{
				"action": "start", "reviewed_body_hash": ticket.BodyHash,
			})
			url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			withSession(req, cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, w.Code, w.Body.String())
			}

			var got db.Ticket
			h.DB.First(&got, "id = ?", ticket.ID)
			if got.Phase != tt.wantPhase {
				t.Errorf("phase = %q, want %q", got.Phase, tt.wantPhase)
			}
			if tt.assignedShem != nil {
				if got.AssignedShem == nil || *got.AssignedShem != *tt.assignedShem {
					t.Errorf("assigned_shem = %v, want unchanged %d", got.AssignedShem, *tt.assignedShem)
				}
			}
		})
	}
}

// TestStartActionEnqueuesGitHubLabelWrite verifies that releasing a
// GitHub-linked pending-approval ticket queues a label outbox row, so the
// issue reflects "unassigned" promptly. There is no "unlinked ticket queues
// nothing" case here (fix round 4 removed it): actionStart's guard now
// requires issue_number IS NOT NULL outright, so an unlinked ticket 409s
// before ever reaching enqueueGitHubPhase — that is covered by
// TestStartActionReleasesPendingApprovalTicket's "not linked to a GitHub
// issue" case instead of by an empty outbox here.
func TestStartActionEnqueuesGitHubLabelWrite(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	n := 7
	ticket := db.Ticket{RepoRemote: "https://github.com/org/repo", Branch: "b",
		Description: "d", BodyHash: ghsync.HashBody("d"),
		Phase: "pending-approval", IssueNumber: &n}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	body, _ := json.Marshal(map[string]string{
		"action": "start", "reviewed_body_hash": ticket.BodyHash,
	})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
	}

	var rows []db.GitHubOutbox
	h.DB.Where("ticket_id = ?", ticket.ID).Find(&rows)
	kinds := map[string]int{}
	for _, row := range rows {
		kinds[row.Kind]++
	}
	if kinds[ghsync.KindLabel] != 1 {
		t.Errorf("label rows = %d, want 1", kinds[ghsync.KindLabel])
	}
}

// TestRequeueActionRejectsPendingApprovalTicket guards against a second,
// unintended door around the intake approval gate (spec Amendment 1
// fix-round-1): "requeue" is a generic "unstick it" action for a ticket a
// shem has already touched, not a substitute for the human review "start"
// performs on a never-run, externally-sourced ticket. It must 409 on a
// pending-approval ticket, and — critically — must leave the phase in the
// database untouched, not just report the right status code.
func TestRequeueActionRejectsPendingApprovalTicket(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "pending-approval"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "requeue"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "pending-approval" {
		t.Errorf("phase = %q, want unchanged pending-approval", got.Phase)
	}
}

// TestNeedsAttentionActionRejectsPendingApprovalTicket guards fix-round-2 of
// the intake approval gate (spec Amendment 1): needs-attention is a fourth
// door, laundered through an intermediate phase — pending-approval ->
// needs-attention -> requeue -> unassigned would release a never-reviewed,
// externally-sourced ticket via two authenticated POSTs. It must 409 on a
// pending-approval ticket, and the phase in the database must be unchanged,
// not just the status code.
func TestNeedsAttentionActionRejectsPendingApprovalTicket(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "pending-approval"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "needs-attention"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "pending-approval" {
		t.Errorf("phase = %q, want unchanged pending-approval", got.Phase)
	}
}

// TestNeedsAttentionAction_MissingTicketReturns404 verifies that a missing
// ticket is reported as 404, not misreported as the 409 the pending-approval
// guard now also returns on RowsAffected == 0.
func TestNeedsAttentionAction_MissingTicketReturns404(t *testing.T) {
	_, mux, cookie := setupActionTest(t)

	body, _ := json.Marshal(map[string]string{"action": "needs-attention"})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/does-not-exist/actions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
