// Package ghsync synchronises GitHub issues with Golem tickets. Inbound work
// (issues to tickets) runs on a slow ingest ticker; outbound work (labels,
// comments, pull requests, issue closes) runs through a transactional outbox
// drained on a fast ticker.
package ghsync

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Outbox row kinds.
const (
	KindComment = "comment"
	KindLabel   = "label"
	KindClose   = "close_issue"
	KindPR      = "pr"
)

// CommentPayload is the JSON payload for a KindComment row.
type CommentPayload struct {
	Body string `json:"body"`
}

// LabelPayload is the JSON payload for a KindLabel row. The worker removes any
// other golem:* label before applying golem:<Phase>.
type LabelPayload struct {
	Phase string `json:"phase"`
}

// ClosePayload is the JSON payload for a KindClose row. It carries no fields;
// the ticket's issue linkage supplies everything needed.
type ClosePayload struct{}

// PRPayload is the JSON payload for a KindPR row.
type PRPayload struct {
	Head  string `json:"head"`
	Base  string `json:"base"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// CommentKey returns the idempotency key for a milestone comment.
func CommentKey(ticketID, milestone string) string {
	return ticketID + ":comment:" + milestone
}

// LabelKey returns the idempotency key for a phase label change. seq is the
// ticket's label-transition ordinal, and it is what makes the key unique per
// phase TRANSITION rather than per (ticket, phase) — finding I4.
//
// The phase graph has cycles: implement -> ready-for-review -> implement is
// an ordinary revise round. Keyed on (ticket, phase) alone, the ticket's
// second arrival at a phase reused its first arrival's key, the insert was
// suppressed, and the issue was left advertising a phase the ticket had
// already left. seq advances only when the phase Golem last queued a label
// for actually changes (see enqueueGitHubPhase in the api package), so a
// genuine retry of the SAME transition — a shem re-sending a PATCH it is not
// sure landed — still computes the same key and is still de-duplicated by
// the unique index.
func LabelKey(ticketID, phase string, seq uint) string {
	return ticketID + ":label:" + phase + ":" + strconv.FormatUint(uint64(seq), 10)
}

// CloseKey returns the idempotency key for closing the linked issue.
func CloseKey(ticketID string) string {
	return ticketID + ":close"
}

// PRKey returns the idempotency key for opening the pull request. Both the
// ready-for-review transition and the branch-pushed callback use this same
// key, so a race between them yields exactly one pull request.
func PRKey(ticketID string) string {
	return ticketID + ":pr"
}

// Enqueue inserts an outbox row. Pass the surrounding transaction as tx so the
// row is committed atomically with the ticket change that caused it.
//
// A row whose IdempotencyKey already exists means this event was already
// queued — by a retry, a crash recovery, or a concurrent writer — and is
// reported as success. That de-duplication is the mechanism that prevents
// duplicate comments.
//
// It is done with ON CONFLICT DO NOTHING rather than by catching the
// constraint violation afterwards, because catching it is only safe on
// SQLite (finding C1). On PostgreSQL the server puts the whole transaction
// into the aborted state the moment it raises 23505: the next statement
// fails with 25P02, and with no next statement Commit itself returns "commit
// unexpectedly resulted in rollback". Since Enqueue runs inside the caller's
// transaction — that is the entire point of the outbox — swallowing the
// error there silently discarded the caller's ticket write. Reproduced
// against PostgreSQL 18.3 through the real handlers: a ticket re-entering a
// phase it had already visited answered 500 and rolled its phase change
// back, and a re-gated ticket could never be approved again.
//
// With DO NOTHING the server raises nothing at all, so no transaction is
// ever aborted, on either backend. isDuplicateKey is kept for
// createTicketFromIssue, whose Create is standalone (its own implicit
// transaction) and therefore poisons nothing.
func Enqueue(tx *gorm.DB, row db.GitHubOutbox) error {
	if row.NextAttempt.IsZero() {
		row.NextAttempt = time.Now()
	}
	err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error
	if err != nil && isDuplicateKey(err) {
		return nil
	}
	return err
}

// MilestoneComment returns the milestone slug and comment body for a phase
// transition, and whether that phase has a milestone at all. The slug becomes
// part of the idempotency key, so it must be stable across releases.
func MilestoneComment(phase, ticketID, baseURL string) (string, string, bool) {
	link := strings.TrimSuffix(baseURL, "/") + "/tickets/" + ticketID
	switch phase {
	case "plan":
		return "spec-written", "Golem wrote a spec for this issue.\n\n" + link, true
	case "implement":
		return "plan-approved", "Plan approved; implementation starting.\n\n" + link, true
	case "ready-for-review":
		return "implementation-complete", "Implementation complete, ready for review.\n\n" + link, true
	default:
		return "", "", false
	}
}

// isDuplicateKey reports whether err is a unique-constraint violation. GORM
// surfaces gorm.ErrDuplicatedKey for drivers that support translation; the
// string checks cover the SQLite and Postgres messages that reach us
// untranslated.
func isDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique_violation")
}

// ParkedRow is one outbox row that has exhausted MaxAttempts: left undone,
// no longer selected by Drain, and therefore never going to reach GitHub
// without an operator asking for it again. TicketTitle comes from the joined
// ticket so the settings page can name the work rather than a UUID.
type ParkedRow struct {
	ID          uint   `gorm:"column:id"`
	TicketID    string `gorm:"column:ticket_id"`
	TicketTitle string `gorm:"column:ticket_title"`
	Kind        string `gorm:"column:kind"`
	Attempts    int    `gorm:"column:attempts"`
	LastError   string `gorm:"column:last_error"`
}

// ParkedRows returns every parked outbox row, oldest first.
//
// Finding I6: publish.go used to claim a parked row was "surfaced in the
// dashboard", and it was not — GitHubOutbox.LastError appeared in no
// template, handler or CLI, and nothing reset Attempts. A parked comment or
// pull-request write was therefore lost permanently behind one log line, and
// because hasParkedOutboxRows stayed true forever, every subsequent 304 poll
// also paid a full reconcile (one GetIssue per linked ticket) for the life of
// the deployment, with no way to clear it. This is the query that makes both
// visible; RetryParkedRow is what clears them.
func ParkedRows(gdb *gorm.DB) ([]ParkedRow, error) {
	var rows []ParkedRow
	err := gdb.Model(&db.GitHubOutbox{}).
		Select("git_hub_outboxes.id, git_hub_outboxes.ticket_id, git_hub_outboxes.kind, "+
			"git_hub_outboxes.attempts, git_hub_outboxes.last_error, tickets.title AS ticket_title").
		Joins("LEFT JOIN tickets ON tickets.id = git_hub_outboxes.ticket_id").
		Where("git_hub_outboxes.done_at IS NULL AND git_hub_outboxes.attempts >= ?", MaxAttempts).
		Order("git_hub_outboxes.id asc").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("query parked outbox rows: %w", err)
	}
	return rows, nil
}

// RetryParkedRow returns one parked row to the queue: attempts back to zero
// and next_attempt brought forward so the next drain pass selects it. It
// never touches done_at — a row that was already delivered stays delivered —
// and it only matches a row that is actually parked, so a double-submitted
// retry cannot reset the counter of a row that is mid-backoff.
//
// It reports whether a row was un-parked, so the caller can tell "already
// retried" from "no such row" without a second query.
func RetryParkedRow(gdb *gorm.DB, id uint) (bool, error) {
	result := gdb.Model(&db.GitHubOutbox{}).
		Where("id = ? AND done_at IS NULL AND attempts >= ?", id, MaxAttempts).
		Updates(map[string]any{
			"attempts":     0,
			"next_attempt": time.Now(),
			"last_error":   "",
		})
	if result.Error != nil {
		return false, fmt.Errorf("retry outbox row %d: %w", id, result.Error)
	}
	return result.RowsAffected > 0, nil
}
