package ghsync

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/config"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// MonitorPullRequests inspects every ticket that has an open pull request and
// acts on what it finds: a failing check or a merge conflict sends the ticket
// back to revising with the problem described, and a pull request that has
// closed ends the watch.
//
// Reaching ready-for-review used to be where Golem stopped. The pull request
// was opened and then nobody looked at it again — CI could go red, the base
// could move underneath it, a reviewer could close it, and the ticket sat in
// ready-for-review indefinitely claiming to be done.
//
// The work is routed through the EXISTING revise path rather than a new one.
// A failing check and a human clicking "request changes" are the same thing
// from the shem's side: feedback on work already pushed, to be fixed on the
// existing branch. api.requestChangesFromReview does exactly that transition,
// so this mirrors it, and the shem needs nothing new to respond.
//
// Runs on the drain ticker rather than the ingest one. Ingest is every 15
// minutes because GitHub's issue list is rate-limited and a slow answer is
// only a slow answer; here a slow answer is a pull request sitting red with
// nobody told, and the calls are per-ticket rather than per-repo.
func (s *Syncer) MonitorPullRequests(ctx context.Context) {
	var tickets []db.Ticket
	// phase != closed rather than phase == ready-for-review: a pull request
	// that closes while the shem is mid-revision still has to end the watch,
	// and only the check/conflict handling below is limited to
	// ready-for-review.
	if err := s.DB.Where("pr_number IS NOT NULL AND phase <> ?", "closed").Find(&tickets).Error; err != nil {
		log.Printf("pr monitor: load tickets: %v", err)
		return
	}
	for _, ticket := range tickets {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := s.monitorPullRequest(ctx, ticket); err != nil {
			log.Printf("pr monitor: ticket %s: %v", ticket.ID, err)
		}
	}
}

func (s *Syncer) monitorPullRequest(ctx context.Context, ticket db.Ticket) error {
	var repo db.GitHubRepo
	if err := s.DB.First(&repo, "repo_remote = ?", ticket.RepoRemote).Error; err != nil {
		return fmt.Errorf("load repo %s: %w", ticket.RepoRemote, err)
	}
	status, err := s.GH.GetPullRequest(ctx, repo.Owner, repo.Name, *ticket.PRNumber)
	if err != nil {
		return err
	}

	// The pull request is finished. Which way it finished decides the
	// ticket: a merge is the work landing, a close without a merge is a
	// person rejecting it, and calling the second one "closed" would record
	// abandoned work as delivered.
	if status.State == "closed" {
		if status.Merged {
			return s.setPhase(ticket, "closed",
				fmt.Sprintf("Pull request #%d was merged.", status.Number))
		}
		return s.setPhase(ticket, "needs-attention",
			fmt.Sprintf("Pull request #%d was closed without merging. "+
				"Nothing further will happen automatically.", status.Number))
	}

	// Anything other than ready-for-review means the shem is already working
	// on this ticket. The checks readable here still describe the head it
	// started from, so acting now would spend an attempt on a fix that is
	// still being written.
	if ticket.Phase != "ready-for-review" {
		return nil
	}

	if status.Conflicted() {
		base := status.BaseRef
		if base == "" {
			base = ticket.BaseBranch
		}
		return s.needsFixing(ticket, fmt.Sprintf(
			"Pull request #%d no longer merges cleanly into %s.\n\n"+
				"Resolve it in the existing worktree, on the existing branch:\n"+
				"  git merge origin/%s\n"+
				"Fix every conflicted file, keeping both sides' intent, then commit the merge. "+
				"Do not rebase and do not start the ticket over.",
			status.Number, base, base))
	}

	failures, err := s.GH.ListFailedChecks(ctx, repo.Owner, repo.Name, status.HeadSHA)
	if err != nil {
		return err
	}
	if len(failures) == 0 {
		return nil
	}
	return s.needsFixing(ticket, describeFailures(status.Number, failures))
}

// describeFailures renders failing checks into the feedback the agent reads.
// It is the agent's only account of what went wrong, so it carries each
// check's name, its conclusion, its own summary where it has one, and the
// URL a human would open.
func describeFailures(prNumber int, failures []github.CheckFailure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d check(s) are failing on pull request #%d.\n\n", len(failures), prNumber)
	for _, f := range failures {
		fmt.Fprintf(&b, "- %s (%s)\n", f.Name, f.Conclusion)
		if sum := strings.TrimSpace(f.Summary); sum != "" {
			fmt.Fprintf(&b, "    %s\n", sum)
		}
		if f.DetailsURL != "" {
			fmt.Fprintf(&b, "    %s\n", f.DetailsURL)
		}
	}
	b.WriteString("\nReproduce each failure locally where you can, fix the cause, and commit " +
		"on the existing branch. Do not start the ticket over, and do not edit CI " +
		"configuration to make a check pass.")
	return b.String()
}

// needsFixing sends the ticket back to revising with feedback, unless it has
// already used its budget of attempts.
func (s *Syncer) needsFixing(ticket db.Ticket, feedback string) error {
	var cfg config.GitHubConfig
	max := cfg.MaxPRFixAttempts()
	if ticket.PRFixAttempts >= max {
		return s.setPhase(ticket, "needs-attention", fmt.Sprintf(
			"Stopping after %d automatic fix attempt(s) — the limit set by "+
				"GOLEM_PR_FIX_ATTEMPTS. The pull request still needs work:\n\n%s",
			ticket.PRFixAttempts, feedback))
	}

	// Phase change, attempt count and feedback commit together. A feedback
	// entry without the phase change is an instruction nobody will act on;
	// a phase change without the count is an attempt that does not count
	// against the cap, which is how the loop this cap exists to stop gets
	// back in.
	return s.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase = ?", ticket.ID, "ready-for-review").
			Updates(map[string]any{
				"phase":           "revising",
				"pr_fix_attempts": ticket.PRFixAttempts + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			// Something moved the ticket between the read and here — a
			// human requesting changes, a requeue. Theirs wins.
			return nil
		}
		return appendLog(tx, ticket.ID, "HUMAN_FEEDBACK", "orchestrator", "developer", feedback)
	})
}

// setPhase moves the ticket and records why, in one transaction.
func (s *Syncer) setPhase(ticket db.Ticket, phase, message string) error {
	if ticket.Phase == phase {
		return nil
	}
	return s.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase = ?", ticket.ID, ticket.Phase).
			Update("phase", phase)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return appendLog(tx, ticket.ID, "STATUS", "orchestrator", "", message)
	})
}
