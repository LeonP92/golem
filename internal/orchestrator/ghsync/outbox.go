// Package ghsync synchronises GitHub issues with Golem tickets. Inbound work
// (issues to tickets) runs on a slow ingest ticker; outbound work (labels,
// comments, pull requests, issue closes) runs through a transactional outbox
// drained on a fast ticker.
package ghsync

import (
	"errors"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
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

// LabelKey returns the idempotency key for a phase label change.
func LabelKey(ticketID, phase string) string {
	return ticketID + ":label:" + phase
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
// A unique-constraint violation on IdempotencyKey means this event was already
// queued — by a retry, a crash recovery, or a concurrent writer — and is
// reported as success. This is the one error the package deliberately
// swallows, and it is the mechanism that prevents duplicate comments.
func Enqueue(tx *gorm.DB, row db.GitHubOutbox) error {
	if row.NextAttempt.IsZero() {
		row.NextAttempt = time.Now()
	}
	err := tx.Create(&row).Error
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
