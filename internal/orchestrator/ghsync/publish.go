package ghsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
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
//
// A row's post-delivery local write (if any — see deliver's postWrite) and
// the done_at commit that finalises it happen in one transaction, via
// commitSuccess. This matters because the GitHub call has already
// irreversibly happened by the time either of those local writes runs: if
// the process crashed (or, more mundanely, one write failed) between the
// GitHub call returning and done_at landing, a row left with done_at still
// NULL is redelivered on the next pass — reposting the same comment, or
// re-invoking any other non-idempotent call. Making the local writes atomic
// doesn't close that gap (the GitHub call itself can never be inside the
// same transaction as a local commit), but it does guarantee that a failure
// on the local side never leaves a *half*-applied result (e.g. a pull
// request linked onto the ticket while the row still looks undelivered) —
// either both local writes land, or neither does and the row is retried like
// any other failure.
func (s *Syncer) Drain(ctx context.Context) error {
	var rows []db.GitHubOutbox
	if err := s.DB.
		Where("done_at IS NULL AND attempts < ? AND next_attempt <= ?", MaxAttempts, time.Now()).
		Order("id asc").Limit(drainBatch).Find(&rows).Error; err != nil {
		return fmt.Errorf("query outbox rows: %w", err)
	}

	for _, row := range rows {
		postWrite, err := s.deliver(ctx, row)
		if err != nil {
			s.recordFailure(row, err)
			continue
		}
		if err := s.commitSuccess(row, postWrite); err != nil {
			// GitHub already accepted the call; only the local commit
			// failed. That must not be silently dropped (it would leave
			// the row eligible for redelivery with no record of the
			// failure) and must not mark the row done — route it through
			// the same retry/backoff/parking path as any other failure.
			s.recordFailure(row, err)
		}
	}
	return nil
}

// commitSuccess finalises a successful delivery: it runs the kind-specific
// local write returned by deliver (postWrite, nil for kinds with none) and
// the done_at update in a single transaction, so a failure of either leaves
// neither applied.
func (s *Syncer) commitSuccess(row db.GitHubOutbox, postWrite func(tx *gorm.DB) error) error {
	now := time.Now()
	return s.DB.Transaction(func(tx *gorm.DB) error {
		if postWrite != nil {
			if err := postWrite(tx); err != nil {
				return err
			}
		}
		if err := tx.Model(&db.GitHubOutbox{}).Where("id = ?", row.ID).
			Updates(map[string]any{"done_at": now, "last_error": ""}).Error; err != nil {
			return fmt.Errorf("mark outbox row %d done: %w", row.ID, err)
		}
		return nil
	})
}

// deliver performs the GitHub call for one row. When the delivery has a
// local write of its own to make (currently only KindPR, recording the
// created pull request's number/URL onto the ticket), deliver does not
// perform that write itself — it returns a closure that Drain runs inside
// the same transaction as marking the row done, via commitSuccess. Kinds
// with no local write beyond done_at return a nil closure.
func (s *Syncer) deliver(ctx context.Context, row db.GitHubOutbox) (func(tx *gorm.DB) error, error) {
	ticket, repo, err := s.linkedIssue(row.TicketID)
	if err != nil {
		return nil, err
	}
	number := *ticket.IssueNumber

	switch row.Kind {
	case KindComment:
		var p CommentPayload
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return nil, fmt.Errorf("decode comment payload for outbox row %d: %w", row.ID, err)
		}
		if err := s.GH.CreateComment(ctx, repo.Owner, repo.Name, number, p.Body); err != nil {
			return nil, fmt.Errorf("create comment on %s#%d: %w", repo.RepoRemote, number, err)
		}
		return nil, nil

	case KindLabel:
		var p LabelPayload
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return nil, fmt.Errorf("decode label payload for outbox row %d: %w", row.ID, err)
		}
		issue, err := s.GH.GetIssue(ctx, repo.Owner, repo.Name, number)
		if err != nil {
			return nil, fmt.Errorf("load issue %s#%d: %w", repo.RepoRemote, number, err)
		}
		return nil, s.applyPhaseLabel(ctx, repo, issue, p.Phase)

	case KindClose:
		if err := s.GH.SetIssueState(ctx, repo.Owner, repo.Name, number, "closed"); err != nil {
			return nil, fmt.Errorf("close %s#%d: %w", repo.RepoRemote, number, err)
		}
		return nil, nil

	case KindPR:
		var p PRPayload
		if err := json.Unmarshal([]byte(row.Payload), &p); err != nil {
			return nil, fmt.Errorf("decode pr payload for outbox row %d: %w", row.ID, err)
		}
		pr, err := s.GH.CreatePullRequest(ctx, repo.Owner, repo.Name,
			p.Head, p.Base, p.Title, p.Body, true)
		if err != nil {
			return nil, fmt.Errorf("create pull request for ticket %s: %w", ticket.ID, err)
		}
		ticketID := ticket.ID
		return func(tx *gorm.DB) error {
			if err := tx.Model(&db.Ticket{}).Where("id = ?", ticketID).
				Updates(map[string]any{"pr_number": pr.Number, "pr_url": pr.HTMLURL}).Error; err != nil {
				return fmt.Errorf("record pull request %d on ticket %s: %w", pr.Number, ticketID, err)
			}
			return nil
		}, nil

	default:
		return nil, fmt.Errorf("unknown outbox kind %q on row %d", row.Kind, row.ID)
	}
}

// applyPhaseLabel removes any other golem:* label from the issue and applies
// golem:<phase>. Labels outside the golem:* namespace — including the
// no-colon opt-in trigger label "golem" itself and any human labels — are
// left completely untouched.
//
// issue is the caller's own, already-fetched read of the issue — this never
// re-fetches it. Both call sites (deliver's KindLabel case, and reconcile)
// already need a current Issue for other reasons before they decide to call
// this, and fetching it here too would double the GetIssue cost of every
// reconcile pass over a non-closed linked ticket for no benefit.
func (s *Syncer) applyPhaseLabel(ctx context.Context, repo db.GitHubRepo, issue github.Issue, phase string) error {
	want := PhaseLabelPrefix + phase
	number := issue.Number

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
