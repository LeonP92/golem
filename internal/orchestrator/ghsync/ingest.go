package ghsync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/slug"
	"gorm.io/gorm"
)

// cursorOverlap is subtracted from the newest issue timestamp when advancing
// the sync cursor, so clock skew between GitHub and the orchestrator cannot
// skip an issue. The resulting re-reads are harmless: creates are blocked by
// the unique index on (repo_remote, issue_number) and updates are idempotent.
const cursorOverlap = time.Minute

// Syncer carries the dependencies shared by ingest, publish, and reconcile.
type Syncer struct {
	DB *gorm.DB
	GH github.Client
}

// NewSyncer returns a Syncer over the given database and GitHub client.
func NewSyncer(gdb *gorm.DB, client github.Client) *Syncer {
	return &Syncer{DB: gdb, GH: client}
}

// IngestRepo pulls issues changed since the repo's cursor and reconciles them
// into tickets. GitHub is the source of truth for Title and Description;
// Phase, Branch, and BaseBranch are Golem-owned and never overwritten here,
// except that a closed issue moves its ticket to phase "closed".
func (s *Syncer) IngestRepo(ctx context.Context, repo *db.GitHubRepo) error {
	since := time.Time{}
	if repo.LastIssueSync != nil {
		since = *repo.LastIssueSync
	}

	page, err := s.GH.ListIssuesSince(ctx, repo.Owner, repo.Name, repo.Label, since, repo.ETag)
	if err != nil {
		s.recordRepoError(repo, err)
		return fmt.Errorf("list issues for %s: %w", repo.RepoRemote, err)
	}

	now := time.Now()
	if page.NotModified {
		if err := s.DB.Model(repo).Updates(map[string]any{
			"last_polled_at": now, "last_error": "",
		}).Error; err != nil {
			return fmt.Errorf("record not-modified poll for %s: %w", repo.RepoRemote, err)
		}

		// A 304 means nothing GitHub-visible changed since the last poll: any
		// GitHub-side drift (a human edits a label, closes an issue) bumps
		// the issue's updated_at, which would have broken the ETag — so
		// skipping reconcile here is safe for that case, and skipping it
		// also avoids paying one GetIssue call per linked ticket on every
		// poll of an otherwise-quiet repo.
		//
		// The one drift this skip cannot see is Golem-side: an outbox row
		// (say, a KindLabel write) that failed MaxAttempts times and parked
		// was never actually applied to GitHub, so the issue's updated_at
		// never moved, the ETag keeps matching, and every future poll keeps
		// returning 304 — forever, on a quiet repo, with nothing to break
		// the loop. Reconcile is the only thing that can still correct that,
		// so it gets a chance to run anyway when this repo has any parked
		// row, at the cost of one cheap COUNT query per 304.
		parked, err := s.hasParkedOutboxRows(repo.RepoRemote)
		if err != nil {
			return fmt.Errorf("check parked outbox rows for %s: %w", repo.RepoRemote, err)
		}
		if !parked {
			return nil
		}
		if err := s.ReconcileRepo(ctx, repo); err != nil {
			return fmt.Errorf("reconcile %s: %w", repo.RepoRemote, err)
		}
		return nil
	}

	newest := since
	var failure error
	for _, issue := range page.Issues {
		if err := s.applyIssue(ctx, repo, issue); err != nil {
			// One bad issue must not abandon the rest of the page — the
			// remaining issues still get applied, and their creates/updates
			// are idempotent so re-processing them next poll is harmless.
			// But the cursor must NOT advance past this failure: if it did,
			// the next poll's `since` would permanently exclude the issue
			// that just failed, with no way to ever pick it up again short
			// of GitHub reporting a fresh update on it.
			log.Printf("ghsync: repo %s issue #%d: %v", repo.RepoRemote, issue.Number, err)
			if failure == nil {
				failure = err
			}
			continue
		}
		if issue.UpdatedAt.After(newest) {
			newest = issue.UpdatedAt
		}
	}

	updates := map[string]any{"last_polled_at": now}
	if failure != nil {
		// Surface the stall so an operator can see why a labeled issue
		// never turned into a ticket, instead of it only reaching the log.
		// Deliberately skip advancing last_issue_sync and etag: retrying the
		// whole page next poll is safe (creates are blocked by the unique
		// (repo_remote, issue_number) index, updates are idempotent), and
		// saving a fresh ETag here could make GitHub answer 304 on the next
		// poll even though this failed issue was never actually applied.
		updates["last_error"] = failure.Error()
	} else {
		updates["last_error"] = ""
		if page.ETag != "" {
			updates["etag"] = page.ETag
		}
		if newest.After(since) {
			cursor := newest.Add(-cursorOverlap)
			updates["last_issue_sync"] = cursor
			repo.LastIssueSync = &cursor
		}
	}
	if err := s.DB.Model(repo).Updates(updates).Error; err != nil {
		return fmt.Errorf("record poll result for %s: %w", repo.RepoRemote, err)
	}

	// Reconciliation runs even when this page had a partial failure
	// (failure != nil above). It does not defeat the partial-failure guard:
	// ReconcileRepo never writes last_issue_sync, etag, or last_error — those
	// were already committed above, cursor/etag untouched — so nothing here
	// can un-freeze the retry the guard set up. Skipping reconcile whenever
	// any single issue in the page failed would instead defeat a different
	// guarantee (see the risk notes on ReconcileRepo): one bad issue must not
	// abandon reconciliation of every other ticket in the repo, which may
	// have synced cleanly. Reconcile also only ever reads a ticket's current,
	// already-committed row, so a ticket whose own write just failed is
	// reconciled against its last known-good state, not against anything the
	// failed write would have changed — there is nothing "half-applied" for
	// reconcile to see. Rides the slow ingest ticker: drift from downtime or
	// a hand-edited label self-heals within one cycle. A parked outbox row
	// is a separate case — see the page.NotModified branch above for why a
	// quiet, 304-ing repo needs its own check to still self-heal one.
	if err := s.ReconcileRepo(ctx, repo); err != nil {
		return fmt.Errorf("reconcile %s: %w", repo.RepoRemote, err)
	}
	return nil
}

// applyIssue creates or updates the ticket linked to one issue.
func (s *Syncer) applyIssue(ctx context.Context, repo *db.GitHubRepo, issue github.Issue) error {
	var ticket db.Ticket
	err := s.DB.Where("repo_remote = ? AND issue_number = ?", repo.RepoRemote, issue.Number).
		First(&ticket).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if issue.State != "open" {
			// Golem does not resurrect issues closed before it saw them.
			return nil
		}
		return s.createTicketFromIssue(ctx, repo, issue)
	}
	if err != nil {
		return fmt.Errorf("look up ticket for issue #%d: %w", issue.Number, err)
	}

	// bodyHash always tracks the CURRENT description (fix round 5). It is
	// written everywhere description is written (here and
	// createTicketFromIssue) so the claim-adjacent predicates in
	// api/tickets.go can require approved_body_hash = body_hash: approval
	// then means "a human approved THIS text", enforced at the point of
	// use, regardless of what phase/intake_approved bookkeeping below does
	// or fails to do. This is what makes the claimed-ticket branch below
	// (which deliberately leaves intake_approved=true and the now-stale
	// approved_body_hash) safe: the moment a claimed-and-edited ticket
	// returns to the pool by ANY path — requeue, the heartbeat reaper, or a
	// future path this file's author never imagined — body_hash no longer
	// matches approved_body_hash, and it is unclaimable until a human
	// re-approves. Guarding requeue/reap individually was the same mistake
	// this task already made three times over; this is the structural fix.
	bodyHash := HashBody(issue.Body)
	updates := map[string]any{
		"title":       issue.Title,
		"description": issue.Body,
		"issue_url":   issue.HTMLURL,
		"body_hash":   bodyHash,
	}

	if issue.State == "closed" {
		// Closing wins outright: it does not matter whether the body also
		// changed, and a closed ticket is never claimable regardless of
		// IntakeApproved, so there is nothing to re-gate.
		if ticket.Phase != "closed" {
			updates["phase"] = "closed"
		}
		if err := s.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update ticket %s for issue #%d: %w", ticket.ID, issue.Number, err)
		}
		return nil
	}

	if !ticket.IntakeApproved || bodyHash == ticket.ApprovedBodyHash {
		// Nothing approved, or approved and unchanged: a plain sync.
		if err := s.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update ticket %s for issue #%d: %w", ticket.ID, issue.Number, err)
		}
		return nil
	}

	// The body changed since approval. Whether to also reset phase and
	// clear intake_approved/approved_body_hash (the UI-visible, "send it
	// back for human re-review" bookkeeping) depends on whether the ticket
	// is still unclaimed — but ticket.AssignedShem was read before this
	// function did anything, so a concurrent claim could land in the
	// window between that read and this write. Checking
	// "assigned_shem IS NULL" in the same update, inside a transaction with
	// the base field sync, decides atomically on the database's current
	// state rather than the stale in-memory read, so this can never leave a
	// ticket both claimed and re-gated back to pending-approval.
	var logMsg string
	txErr := s.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(updates).Error; err != nil {
			return fmt.Errorf("update ticket %s for issue #%d: %w", ticket.ID, issue.Number, err)
		}
		regate := tx.Model(&db.Ticket{}).
			Where("id = ? AND assigned_shem IS NULL", ticket.ID).
			Updates(map[string]any{
				"intake_approved":    false,
				"approved_body_hash": "",
				"phase":              "pending-approval",
			})
		if regate.Error != nil {
			return regate.Error
		}
		if regate.RowsAffected > 0 {
			// Not yet claimed: the approval was for the old text, and
			// nobody has started work on the strength of it yet, so pull it
			// back for a human to re-review the new text.
			logMsg = "Issue body changed after approval; a human must re-approve before this ticket can be claimed."
		} else {
			// Already claimed (whether at the outer read or by a race that
			// landed just now): the assigned shem already has the
			// previously-approved text and may be mid-run. Do not yank the
			// ticket out from under it — body_hash above already makes it
			// unclaimable again once it returns to the pool; just make the
			// change loudly visible in its own log.
			logMsg = "Issue body changed after approval; this ticket is already claimed, so the running agent is still working from the previously approved text."
		}
		return nil
	})
	if txErr != nil {
		return txErr
	}
	if err := appendLog(s.DB, ticket.ID, "STATUS", "system", "", logMsg); err != nil {
		return fmt.Errorf("log post-approval edit for ticket %s: %w", ticket.ID, err)
	}
	return nil
}

// createTicketFromIssue opens a new ticket for an issue, gated in
// pending-approval. A human releases it to unassigned from the dashboard
// before any shem can claim it through the existing /api/tickets/available
// path.
func (s *Syncer) createTicketFromIssue(ctx context.Context, repo *db.GitHubRepo, issue github.Issue) error {
	base, err := s.GH.DefaultBranch(ctx, repo.Owner, repo.Name)
	if err != nil || base == "" {
		base = "main"
	}
	id := uuid.NewString()
	number := issue.Number
	ticket := db.Ticket{
		ID:          id,
		RepoRemote:  repo.RepoRemote,
		BaseBranch:  base,
		Title:       issue.Title,
		Branch:      slug.Branch(issue.Title, id),
		Description: issue.Body,
		BodyHash:    HashBody(issue.Body),
		// Externally-sourced tickets are NOT claimable on arrival. The issue
		// body is authored by anyone who can open an issue in this repo, and it
		// is interpolated into the agent prompts that drive brainstorm, plan,
		// implement, and revise — one of which has shell and repo write access.
		// A human releases the ticket to "unassigned" from the dashboard after
		// reading it. See spec Amendment 1.
		Phase:       "pending-approval",
		IssueNumber: &number,
		IssueURL:    issue.HTMLURL,
	}
	createErr := s.DB.Create(&ticket).Error
	if createErr != nil && isDuplicateKey(createErr) {
		// A concurrent pass already created this ticket; its row is the one
		// that counts.
		return nil
	}
	if createErr != nil {
		return fmt.Errorf("create ticket for issue #%d: %w", issue.Number, createErr)
	}
	return nil
}

// recordRepoError stores a poll failure on the repo row so the dashboard can
// show it. A failure for one repo never stops the others. The write is
// best-effort: if it fails too, the original GitHub error (already returned
// to the caller) is what matters, so this only logs rather than compounding
// the error return.
func (s *Syncer) recordRepoError(repo *db.GitHubRepo, err error) {
	now := time.Now()
	if uerr := s.DB.Model(repo).Updates(map[string]any{
		"last_polled_at": now,
		"last_error":     err.Error(),
	}).Error; uerr != nil {
		log.Printf("ghsync: repo %s: failed to record poll error: %v", repo.RepoRemote, uerr)
	}
}
