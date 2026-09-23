package ghsync

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// HashDescription returns the hex-encoded SHA-256 hash of a ticket
// description. It binds an intake approval (spec Amendment 1) to the exact
// text a human reviewed: actionStart (api package) records
// HashDescription(ticket.Description) as db.Ticket.ApprovedBodyHash when it
// sets IntakeApproved, and applyIssue (this package) recomputes it on every
// poll to detect a post-approval edit. Both call sites must use this same
// function, over the same bytes, or the two hashes are not comparable.
//
// THE INVARIANT, which is what makes the gate sound:
//
//	db.Ticket.BodyHash == HashDescription(db.Ticket.Description)
//
// at every site that writes either column. It is the reason this takes a
// DESCRIPTION and not a body. It was HashBody(issue.Body) until the
// orchestrator started storing github.Issue.TicketDescription() — title, a
// blank line, then body — as the description, for CLI parity and because an
// issue whose title says everything otherwise produced an empty agent task.
//
// The moment the title became part of the description it became part of what
// reaches an agent prompt, and the requirement is that the hash cover EVERY
// byte of untrusted issue text that can reach one. Hashing the body alone
// while showing and sending title+body was a live hole, not a safe
// simplification: a title edited between the approval page rendering and the
// operator clicking Approve moved no hash, so the reviewed-hash check passed
// and the agent received a title nobody read.
//
// Anything added to the description in future must be hashed here too, by
// construction — compose it inside Issue.TicketDescription and this stays
// true on its own.
func HashDescription(description string) string {
	sum := sha256.Sum256([]byte(description))
	return hex.EncodeToString(sum[:])
}

// appendLog inserts a LogEntry for ticketID with a server-assigned
// sequence_num. This mirrors api.Handlers.appendLog's logic; ghsync cannot
// import the api package (api already imports ghsync, so that would be a
// dependency cycle), so this is a small, separate copy scoped to the one
// call site that needs it here: recording a post-approval issue-body edit.
func appendLog(gdb *gorm.DB, ticketID, entryType, fromRole, toRole, message string) error {
	var maxSeq struct{ Max *uint }
	// The error is returned, not discarded: on a failure maxSeq.Max stays
	// nil, nextSeq falls back to 1, and the Create below violates
	// idx_ticket_seq for any ticket that already has a log entry. It fails
	// closed, but the operator is shown a unique-constraint violation
	// instead of the real cause, and — because appendLog's caller is
	// applyIssue — the whole page is marked as a partial failure and the
	// ingest cursor freezes on a misleading error.
	if err := gdb.Model(&db.LogEntry{}).
		Select("MAX(sequence_num) as max").
		Where("ticket_id = ?", ticketID).
		Scan(&maxSeq).Error; err != nil {
		return fmt.Errorf("next log sequence for ticket %s: %w", ticketID, err)
	}
	nextSeq := uint(1)
	if maxSeq.Max != nil {
		nextSeq = *maxSeq.Max + 1
	}
	entry := db.LogEntry{
		TicketID:    ticketID,
		SequenceNum: nextSeq,
		EntryType:   entryType,
		FromRole:    fromRole,
		ToRole:      toRole,
		Message:     message,
		CreatedAt:   time.Now(),
	}
	return gdb.Create(&entry).Error
}
