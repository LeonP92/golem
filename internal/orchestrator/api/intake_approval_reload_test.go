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

// TestActionStartBindsApprovalToTheTextInItsOwnTransaction covers finding I5.
// actionStart read the ticket with a plain, pre-transaction h.DB.First and
// hashed that snapshot's Description inside the transaction. An ingest write
// landing in the gap was therefore written into the approval as
// approved_body_hash = H(old) while body_hash had already moved to H(new).
//
// The claim predicates require approved_body_hash = body_hash, so that failed
// closed — but with no in-product recovery: the dashboard showed the ticket as
// released, a second start 409ed because intake_approved was already true, and
// requeue excludes unassigned. A human had to edit the GitHub issue to unstick
// it.
//
// Verified against HEAD e8a4a67 before the fix: approved_body_hash was the
// hash of the OLD text, the ticket was not claimable, and /available returned
// it zero times.
func TestActionStartBindsApprovalToTheTextInItsOwnTransaction(t *testing.T) {
	h, mux, cookie := setupActionTest(t)
	h.RegisterTicketRoutes(mux)
	shem := seedShem(t, h, "reload-shem", "reloadkey")

	number := 61
	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Title: "t", Branch: "b",
		Description: "old text", BodyHash: ghsync.HashBody("old text"),
		Phase: "pending-approval", IssueNumber: &number,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	// An ingest pass commits new issue text in the window between
	// actionStart's pre-transaction read and its transaction.
	interleaveOnceAfterTicketRead(t, h.DB, func(gdb *gorm.DB) {
		gdb.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(map[string]any{
			"description": "new text",
			"body_hash":   ghsync.HashBody("new text"),
		})
	})

	body, _ := json.Marshal(map[string]string{"action": "start"})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	withSession(req, cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("start status = %d, want 204: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	if err := h.DB.First(&got, "id = ?", ticket.ID).Error; err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if got.ApprovedBodyHash != got.BodyHash {
		t.Errorf("approved_body_hash = %q, body_hash = %q — approval bound to a snapshot the transaction had already superseded",
			got.ApprovedBodyHash, got.BodyHash)
	}
	// body_hash is owned by ingest alone. actionStart must never write it:
	// stamping both hashes from one read is what would make new, unreviewed
	// text claimable.
	if got.BodyHash != ghsync.HashBody("new text") {
		t.Errorf("body_hash = %q, want the ingest-written hash of %q — actionStart must not write body_hash",
			got.BodyHash, "new text")
	}

	// A released ticket must actually be claimable; that is the property the
	// stale hash silently destroyed.
	if _, err := h.ClaimTicket(ticket.ID, shem.ID); err != nil {
		t.Errorf("claim after approval: %v — the released ticket is not claimable", err)
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
