package ghsync

import (
	"context"
	"fmt"
	"log"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// ReconcileRepo corrects drift between a repo's linked tickets and their
// GitHub issues.
//
// Only *diffable* state is reconciled: the golem:<phase> label and the
// issue's open/closed state. Comments and pull requests are one-shot events
// with no end state to compare against — they belong to the outbox alone,
// and re-sending them here would post duplicates to a real issue. Reconcile
// never calls CreateComment or CreatePullRequest, and it never writes to the
// ticket row: IntakeApproved, ApprovedBodyHash, BodyHash, and Phase itself
// are all left exactly as ingest and the API handlers set them. A ticket
// sitting in pending-approval stays there — reconcile applies whatever
// golem:<phase> label matches its current phase (including
// "golem:pending-approval", which is a desirable signal to the issue author
// that Golem is waiting on a human) but never releases it.
//
// This is also the only place a lost trigger label is observable: the
// ingest list query is filtered by label, so an issue that loses it simply
// stops appearing there and cannot be distinguished from one that never
// changed. Losing the trigger label is not a cancel signal — cancelling
// stays a deliberate UI action — so reconcile only logs it and continues
// reconciling that ticket normally.
//
// One ticket's failure (a deleted issue, a transient GitHub error) must not
// abandon the rest of the repo's tickets, so reconcileTicket's errors are
// logged and swallowed per ticket rather than aborting the loop.
func (s *Syncer) ReconcileRepo(ctx context.Context, repo *db.GitHubRepo) error {
	var tickets []db.Ticket
	if err := s.DB.
		Where("repo_remote = ? AND issue_number IS NOT NULL", repo.RepoRemote).
		Find(&tickets).Error; err != nil {
		return fmt.Errorf("query linked tickets for %s: %w", repo.RepoRemote, err)
	}

	for _, ticket := range tickets {
		if err := s.reconcileTicket(ctx, repo, ticket); err != nil {
			log.Printf("ghsync: reconcile ticket %s (issue #%d): %v",
				ticket.ID, *ticket.IssueNumber, err)
		}
	}
	return nil
}

// reconcileTicket brings one issue's diffable state in line with its ticket.
func (s *Syncer) reconcileTicket(ctx context.Context, repo *db.GitHubRepo, ticket db.Ticket) error {
	number := *ticket.IssueNumber
	issue, err := s.GH.GetIssue(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("get issue #%d: %w", number, err)
	}

	if repo.Label != "" && !issue.HasLabel(repo.Label) {
		// The trigger label is gone, so ingest's label-filtered list query
		// will never surface this issue again — this reconcile pass is the
		// only place that can ever observe the removal. It is deliberately
		// not treated as a cancel signal: log it and keep reconciling this
		// ticket exactly as if the label were still present.
		log.Printf("ghsync: issue #%d lost the %q trigger label; ticket %s continues",
			number, repo.Label, ticket.ID)
	}

	if ticket.Phase == "closed" {
		// Closing a ticket only ever enqueues KindClose (see
		// api/human.go's actionClose) — never a "golem:closed" label — so
		// reconcile mirrors that and touches only issue state here.
		if issue.State == "closed" {
			return nil
		}
		// The issue is open and the ticket is closed, which on this path
		// almost always means a human reopened the issue. Golem has no
		// reopen action, so reconcile undoes that every poll; log it rather
		// than doing it silently, so the person who keeps finding their
		// issue closed again has something to find (m10). Honouring the
		// reopen instead would need a product decision about what a
		// reopened, already-closed ticket becomes.
		log.Printf("ghsync: issue #%d is open but ticket %s is closed; re-closing it "+
			"(Golem has no reopen action — close the ticket's GitHub issue from Golem, or "+
			"open a new issue, rather than reopening this one)", number, ticket.ID)
		if err := s.GH.SetIssueState(ctx, repo.Owner, repo.Name, number, "closed"); err != nil {
			return fmt.Errorf("close issue #%d: %w", number, err)
		}
		return nil
	}

	if err := s.applyPhaseLabel(ctx, *repo, issue, ticket.Phase); err != nil {
		return fmt.Errorf("apply phase label for issue #%d: %w", number, err)
	}
	return nil
}

// hasParkedOutboxRows reports whether repoRemote has any outbox row parked
// at MaxAttempts (done_at still NULL — Drain gives up retrying but never
// marks a parked row done). A parked row never reached GitHub, so it is the
// one kind of drift a page.NotModified response cannot rule out: the
// issue's updated_at never moved, so the ETag keeps matching and every
// later poll keeps getting the same 304 GitHub gave this one. IngestRepo
// uses this to decide whether reconcile still needs to run despite the 304.
func (s *Syncer) hasParkedOutboxRows(repoRemote string) (bool, error) {
	var count int64
	err := s.DB.Model(&db.GitHubOutbox{}).
		Joins("JOIN tickets ON tickets.id = git_hub_outboxes.ticket_id").
		Where("tickets.repo_remote = ? AND git_hub_outboxes.done_at IS NULL AND git_hub_outboxes.attempts >= ?",
			repoRemote, MaxAttempts).
		Count(&count).Error
	if err != nil {
		return false, fmt.Errorf("count parked outbox rows for %s: %w", repoRemote, err)
	}
	return count > 0, nil
}
