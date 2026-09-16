package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// interleaveOnceAfterTicketRead runs apply exactly once, immediately after the
// first SELECT this handle makes against the tickets table. It stands in for a
// concurrent ghsync.applyIssue commit landing in the window between a
// handler's pre-transaction read and the transaction that follows it — the
// same technique the correctness review used to reproduce finding I5, and the
// only way to force that READ COMMITTED interleaving deterministically.
func interleaveOnceAfterTicketRead(t *testing.T, gdb *gorm.DB, apply func(gdb *gorm.DB)) {
	t.Helper()
	var once sync.Once
	name := "test:interleave_after_ticket_read"
	err := gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Table != "tickets" {
			return
		}
		once.Do(func() { apply(gdb) })
	})
	if err != nil {
		t.Fatalf("register callback: %v", err)
	}
	t.Cleanup(func() {
		if err := gdb.Callback().Query().Remove(name); err != nil {
			t.Errorf("remove callback: %v", err)
		}
	})
}

// TestActionStartChecksTheReviewedHashInsideItsOwnTransaction is where fix
// rounds 1b and 1c meet.
//
// Round 1b (finding I5): actionStart read the ticket with a plain,
// pre-transaction h.DB.First and hashed that snapshot's Description inside
// the transaction, so an ingest write landing in the gap was written into the
// approval as approved_body_hash = H(old) while body_hash had already moved
// to H(new). The claim predicates require the two to be equal, so it failed
// closed — but with no in-product recovery: the dashboard showed the ticket as
// released, a second start 409ed on intake_approved, and requeue excludes
// unassigned. A human had to edit the GitHub issue to unstick it.
//
// Round 1c: the approval now carries the body_hash the operator's page was
// rendered from, and actionStart requires it to still hold. This test forces
// the same narrow read-to-transaction interleaving and asserts the two halves
// together: the check must be made against the row as re-read INSIDE the
// transaction, and a mismatch must refuse cleanly and recoverably.
//
// That it uses the reloaded row and not the pre-transaction snapshot is the
// whole point. Comparing against the snapshot would see the OLD hash on both
// sides here, pass, and approve the new text — reinstating I5's defect in a
// form that fails open. A plain-looking "optimisation" to compare
// reviewedBodyHash against the already-loaded ticket variable does exactly
// that, and only an interleaving inside the request catches it.
func TestActionStartChecksTheReviewedHashInsideItsOwnTransaction(t *testing.T) {
	h, mux, cookie := setupActionTest(t)
	h.RegisterTicketRoutes(mux)
	shem := seedShem(t, h, "reload-shem", "reloadkey")

	number := 61
	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: "old text", BodyHash: ghsync.HashDescription("old text"),
		Phase: "pending-approval", IssueNumber: &number,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	reviewed := ticket.BodyHash // what the operator's page rendered

	// An ingest pass commits new issue text in the window between
	// actionStart's pre-transaction read and its transaction.
	interleaveOnceAfterTicketRead(t, h.DB, func(gdb *gorm.DB) {
		gdb.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(map[string]any{
			"description": "new text",
			"body_hash":   ghsync.HashDescription("new text"),
		})
	})

	body, _ := json.Marshal(map[string]string{
		"action": "start", "reviewed_body_hash": reviewed,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("start status = %d, want 409 — the text changed inside the request: %s",
			w.Code, w.Body.String())
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.IntakeApproved || got.ApprovedBodyHash != "" || got.Phase != "pending-approval" {
		t.Errorf("after the refusal: intake_approved=%v approved_body_hash=%q phase=%q, want false/empty/pending-approval",
			got.IntakeApproved, got.ApprovedBodyHash, got.Phase)
	}
	// body_hash is owned by ingest alone. actionStart must never write it:
	// stamping both hashes from one read is what would make new, unreviewed
	// text claimable.
	if got.BodyHash != ghsync.HashDescription("new text") {
		t.Errorf("body_hash = %q, want the ingest-written hash of %q — actionStart must not write body_hash",
			got.BodyHash, "new text")
	}
	if _, err := h.ClaimTicket(ticket.ID, shem.ID); err == nil {
		t.Error("ticket is claimable after a refused approval")
	}

	// Recovery is a reload, and the approval it produces leaves the ticket
	// genuinely claimable — the liveness half of I5.
	body, _ = json.Marshal(map[string]string{
		"action": "start", "reviewed_body_hash": ghsync.HashDescription("new text"),
	})
	req = httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("re-approval after reload = %d, want 204: %s", w.Code, w.Body.String())
	}
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.ApprovedBodyHash != got.BodyHash {
		t.Errorf("approved_body_hash = %q, body_hash = %q — want them equal", got.ApprovedBodyHash, got.BodyHash)
	}
	if _, err := h.ClaimTicket(ticket.ID, shem.ID); err != nil {
		t.Errorf("claim after re-approval: %v — the released ticket is not claimable", err)
	}
}

// TestActionCloseDecidesFromItsOwnTransaction covers minor m11, the same
// defect class as I5 in the one other handler that still had it: actionClose
// took a plain pre-transaction read and used it to decide, inside the
// transaction, whether the ticket had a linked issue to close.
//
// Verified against HEAD e8a4a67 before the fix: 0 close_issue rows.
func TestActionCloseDecidesFromItsOwnTransaction(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: "d", Phase: "implement",
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	// The issue link appears in the window between the pre-transaction read
	// and the transaction.
	interleaveOnceAfterTicketRead(t, h.DB, func(gdb *gorm.DB) {
		gdb.Model(&db.Ticket{}).Where("id = ?", ticket.ID).
			Update("issue_number", 62)
	})

	body, _ := json.Marshal(map[string]string{"action": "close"})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("close status = %d, want 204: %s", w.Code, w.Body.String())
	}

	var closes int64
	h.DB.Model(&db.GitHubOutbox{}).
		Where("ticket_id = ? AND kind = ?", ticket.ID, ghsync.KindClose).Count(&closes)
	if closes != 1 {
		t.Errorf("close_issue rows = %d, want 1 — the close decision came from a superseded snapshot", closes)
	}
}
