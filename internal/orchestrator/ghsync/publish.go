package ghsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// MaxAttempts is the number of failed deliveries after which an outbox row is
// parked: left undone, retried no further, and surfaced in the dashboard.
const MaxAttempts = 8

// PhaseLabelPrefix is the namespace Golem owns on GitHub issues. Labels
// outside it are never added, removed, or overwritten.
const PhaseLabelPrefix = "golem:"

// drainBatch is the maximum number of rows handled in one pass, so a large
// backlog cannot monopolise the ticker.
const drainBatch = 50

// maxBackoffShift bounds the left-shift in Backoff. 2^20 minutes is already
// many orders of magnitude past the 30-minute cap, so clamping the shift here
// keeps the arithmetic (and any future MaxAttempts increase) far from
// int64/time.Duration overflow while leaving every capped value unchanged.
const maxBackoffShift = 20

// Backoff returns the delay before retry number attempts: 1m, 2m, 4m, 8m …
// capped at 30 minutes. attempts is clamped to 1 (an attempts value below 1
// is not meaningful — the first attempt is attempts==1).
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > maxBackoffShift {
		shift = maxBackoffShift
	}
	d := time.Minute << uint(shift)
	if d <= 0 || d > 30*time.Minute {
		return 30 * time.Minute
	}
	return d
}

// Drain delivers every outbox row that is due. A row that fails is retried
// later with exponential backoff; after MaxAttempts it is parked. Delivery
// errors never abort the pass — one stuck row must not block the queue.
func (s *Syncer) Drain(ctx context.Context) error {
	var rows []db.GitHubOutbox
	if err := s.DB.
		Where("done_at IS NULL AND attempts < ? AND next_attempt <= ?", MaxAttempts, time.Now()).
		Order("id asc").Limit(drainBatch).Find(&rows).Error; err != nil {
		return fmt.Errorf("query outbox rows: %w", err)
	}

	for _, row := range rows {
		if err := s.deliver(ctx, row); err != nil {
			s.recordFailure(row, err)
			continue
		}
		s.recordSuccess(row)
	}
	return nil
}

// recordSuccess marks a row done. The update is best-effort: if it fails, the
// row will be redelivered on the next pass rather than silently vanishing, so
// the failure is logged rather than swallowed.
func (s *Syncer) recordSuccess(row db.GitHubOutbox) {
	now := time.Now()
	if err := s.DB.Model(&db.GitHubOutbox{}).Where("id = ?", row.ID).
		Updates(map[string]any{"done_at": now, "last_error": ""}).Error; err != nil {
		log.Printf("ghsync: outbox row %d delivered but could not be marked done: %v", row.ID, err)
	}
}

// deliver performs the GitHub call for one row.
func (s *Syncer) deliver(ctx context.Context, row db.GitHubOutbox) error {
	ticket, repo, err := s.linkedIssue(row.TicketID)
	if err != nil {
		return err
	}
	number := *ticket.IssueNumber

	switch row.Kind {
	case KindComment:
		var p CommentPayload
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return fmt.Errorf("decode comment payload for outbox row %d: %w", row.ID, err)
		}
		if err := s.GH.CreateComment(ctx, repo.Owner, repo.Name, number, p.Body); err != nil {
			return fmt.Errorf("create comment on %s#%d: %w", repo.RepoRemote, number, err)
		}
		return nil

	case KindLabel:
		var p LabelPayload
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return fmt.Errorf("decode label payload for outbox row %d: %w", row.ID, err)
		}
		return s.applyPhaseLabel(ctx, repo, number, p.Phase)

	case KindClose:
		if err := s.GH.SetIssueState(ctx, repo.Owner, repo.Name, number, "closed"); err != nil {
			return fmt.Errorf("close %s#%d: %w", repo.RepoRemote, number, err)
		}
		return nil

	case KindPR:
		var p PRPayload
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return fmt.Errorf("decode pr payload for outbox row %d: %w", row.ID, err)
		}
		pr, err := s.GH.CreatePullRequest(ctx, repo.Owner, repo.Name,
			p.Head, p.Base, p.Title, p.Body, true)
		if err != nil {
			return fmt.Errorf("create pull request for ticket %s: %w", ticket.ID, err)
		}
		if err := s.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).
			Updates(map[string]any{"pr_number": pr.Number, "pr_url": pr.HTMLURL}).Error; err != nil {
			return fmt.Errorf("record pull request %d on ticket %s: %w", pr.Number, ticket.ID, err)
		}
		return nil

	default:
		return fmt.Errorf("unknown outbox kind %q on row %d", row.Kind, row.ID)
	}
}

// applyPhaseLabel removes any other golem:* label from the issue and applies
// golem:<phase>. Labels outside the golem:* namespace — including the
// no-colon opt-in trigger label "golem" itself and any human labels — are
// left completely untouched.
func (s *Syncer) applyPhaseLabel(ctx context.Context, repo db.GitHubRepo, number int, phase string) error {
	want := PhaseLabelPrefix + phase

	issue, err := s.GH.GetIssue(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("load issue %s#%d: %w", repo.RepoRemote, number, err)
	}
	for _, l := range issue.Labels {
		if l == want || !strings.HasPrefix(l, PhaseLabelPrefix) {
			continue
		}
		if err := s.GH.RemoveLabel(ctx, repo.Owner, repo.Name, number, l); err != nil {
			return fmt.Errorf("remove stale label %q from %s#%d: %w", l, repo.RepoRemote, number, err)
		}
	}
	if issue.HasLabel(want) {
		return nil
	}
	if err := s.GH.AddLabel(ctx, repo.Owner, repo.Name, number, want); err != nil {
		return fmt.Errorf("add label %q to %s#%d: %w", want, repo.RepoRemote, number, err)
	}
	return nil
}

// linkedIssue loads the ticket and its repo settings, verifying the ticket is
// actually linked to an issue.
func (s *Syncer) linkedIssue(ticketID string) (db.Ticket, db.GitHubRepo, error) {
	var ticket db.Ticket
	if err := s.DB.First(&ticket, "id = ?", ticketID).Error; err != nil {
		return ticket, db.GitHubRepo{}, fmt.Errorf("load ticket %s: %w", ticketID, err)
	}
	if ticket.IssueNumber == nil {
		return ticket, db.GitHubRepo{}, fmt.Errorf("ticket %s has no linked issue", ticketID)
	}
	var repo db.GitHubRepo
	if err := s.DB.First(&repo, "repo_remote = ?", ticket.RepoRemote).Error; err != nil {
		return ticket, repo, fmt.Errorf("load repo %s: %w", ticket.RepoRemote, err)
	}
	return ticket, repo, nil
}

// recordFailure increments the attempt counter and schedules the retry. At
// MaxAttempts the row is left undone and stops being selected by Drain's
// query — parked, and visible in the dashboard through LastError. The update
// itself is best-effort: a failure to persist it is logged rather than
// swallowed, since silently dropping it would leave the row retrying with a
// stale NextAttempt and no backoff.
func (s *Syncer) recordFailure(row db.GitHubOutbox, cause error) {
	attempts := row.Attempts + 1
	updates := map[string]any{
		"attempts":     attempts,
		"last_error":   cause.Error(),
		"next_attempt": time.Now().Add(Backoff(attempts)),
	}
	if attempts >= MaxAttempts {
		log.Printf("ghsync: parking outbox row %d (ticket %s, kind %s) after %d attempts: %v",
			row.ID, row.TicketID, row.Kind, attempts, cause)
	}
	if err := s.DB.Model(&db.GitHubOutbox{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		log.Printf("ghsync: outbox row %d failed delivery (%v) and the failure could not be recorded: %v",
			row.ID, cause, err)
	}
}
