package ghsync

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// HashBody returns the hex-encoded SHA-256 hash of an issue body. It binds
// an intake approval (spec Amendment 1) to the exact text a human reviewed:
// actionStart (api package) records HashBody(description) as
// db.Ticket.ApprovedBodyHash when it sets IntakeApproved, and applyIssue
// (this package) recomputes it on every poll to detect a post-approval edit.
// Both call sites must use this same function so the two hashes are ever
// comparable.
func HashBody(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// appendLog inserts a LogEntry for ticketID with a server-assigned
// sequence_num. This mirrors api.Handlers.appendLog's logic; ghsync cannot
// import the api package (api already imports ghsync, so that would be a
// dependency cycle), so this is a small, separate copy scoped to the one
// call site that needs it here: recording a post-approval issue-body edit.
func appendLog(gdb *gorm.DB, ticketID, entryType, fromRole, toRole, message string) error {
	var maxSeq struct{ Max *uint }
	gdb.Model(&db.LogEntry{}).
		Select("MAX(sequence_num) as max").
		Where("ticket_id = ?", ticketID).
		Scan(&maxSeq)
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
