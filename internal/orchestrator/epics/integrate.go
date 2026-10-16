package epics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// The integration lane merges child pull requests itself when the epic
// allows it. Three rules keep it safe:
//
//  1. Evidence before the call: the attempt row is written BEFORE the merge
//     request, so a crash between the two leaves a "merging" row the next
//     pass reconciles from the remote — never a blind re-merge.
//  2. One fenced lane per target ref: a named lease means two orchestrator
//     processes (or two monitor ticks) cannot merge the same PR twice.
//  3. Fail closed with a named cause: every refusal (stale head, wrong
//     base, branch protection, missing tester verdict) is recorded on the
//     attempt row and nothing merges.

const laneLeaseTTL = 2 * time.Minute

// AttemptMerge runs the lane for one child pull request the monitor just
// observed as clean (open, merging, checks green, ticket past the review
// gate). It is a no-op for ordinary tickets and for epics that merge by
// hand, so the monitor can call it unconditionally.
func AttemptMerge(ctx context.Context, gdb *gorm.DB, gh github.Client, ticket *db.Ticket, status github.PullRequestStatus) error {
	if ticket.EpicID == nil || ticket.PRNumber == nil {
		return nil
	}
	var e db.Epic
	if err := gdb.First(&e, "id = ?", *ticket.EpicID).Error; err != nil {
		return fmt.Errorf("epics: lane load epic: %w", err)
	}
	if !e.AutoMergeChildren || e.State != "running" {
		return nil
	}
	// No failed checks is not green: pending CI, branch protection, a
	// draft or uncomputed mergeability all wait for a later tick.
	clean := status.Mergeable != nil && *status.Mergeable && status.MergeableState == "clean"
	if !clean && !status.Conflicted() {
		return nil
	}

	// The tester gate is fail closed. Only a pass verdict recorded against
	// this exact head authorizes the merge; a fail or inconclusive blocks
	// with the verdict as the named cause, and no verdict at all blocks
	// until the tester produces one. A verdict for a different head is
	// stale evidence about code that no longer exists — ignored, never
	// honored.
	if e.TesterRequired {
		v := latestVerdict(gdb, e.ID, ticket.EpicNodeKey, status.HeadSHA)
		switch {
		case v == nil:
			return recordOnce(gdb, &e, ticket, status, "blocked-tester",
				"auto-merge requires a tester verdict for this head; none exists yet")
		case v.Verdict != "pass":
			return recordOnce(gdb, &e, ticket, status, "blocked-tester",
				fmt.Sprintf("tester verdict for this head is %q: %s", v.Verdict, v.Evidence))
		}
	}

	targetRef := e.IntegrationRef
	if targetRef == "" {
		targetRef = e.BaseBranch
	}
	// A pull request targeting anything but the plan's integration ref is
	// stale against the approved plan: merging it would land the work
	// somewhere readiness does not count.
	if status.BaseRef != "" && status.BaseRef != targetRef {
		return recordOnce(gdb, &e, ticket, status, "failed-stale",
			fmt.Sprintf("pull request targets %s; the approved plan integrates into %s", status.BaseRef, targetRef))
	}

	leaseName := "integration:" + e.RepoRemote + ":" + targetRef
	holder := fmt.Sprintf("lane-%d", timeNow().UnixNano())
	token, acquired, err := db.AcquireLease(gdb, leaseName, holder, laneLeaseTTL)
	if err != nil {
		return fmt.Errorf("epics: lane lease: %w", err)
	}
	if !acquired {
		// Another pass owns the lane right now. The monitor ticks again;
		// losing the lease is not an error.
		return nil
	}
	defer func() { _ = db.ReleaseLease(gdb, leaseName, holder, token) }()

	if err := reconcileAttempts(ctx, gdb, gh, &e, ticket); err != nil {
		return err
	}

	// The monitor already dispatched a conflict fix for this head; the
	// lane only records the evidence, once per head.
	if status.Conflicted() {
		return recordOnce(gdb, &e, ticket, status, "failed-conflict",
			"pull request no longer merges cleanly; fix dispatched")
	}

	// A transient failure (failed-unknown) on this same head may retry:
	// reopen that row. Every other terminal state is final for the head —
	// merged is done, and the failures all require the head (or a human)
	// to change before merging could be right.
	reopened := gdb.Model(&db.IntegrationAttempt{}).
		Where("epic_id = ? AND epic_node_key = ? AND head_sha = ? AND state = ?",
			e.ID, ticket.EpicNodeKey, status.HeadSHA, "failed-unknown").
		Updates(map[string]any{"state": "merging", "cause": "", "merge_sha": ""})
	if reopened.Error != nil {
		return reopened.Error
	}
	if reopened.RowsAffected == 0 {
		att := db.IntegrationAttempt{
			EpicID: e.ID, EpicNodeKey: ticket.EpicNodeKey, TicketID: ticket.ID,
			PRNumber: *ticket.PRNumber, HeadSHA: status.HeadSHA, BaseSHA: e.BaseSHA,
			State: "merging",
		}
		if err := gdb.Create(&att).Error; err != nil {
			// A terminal row for this exact head already exists: one
			// attempt per head is the bound, not a bug.
			return nil
		}
	}

	// A pass that outlived its lease must not merge after a newer holder.
	if ok, ferr := db.CheckFencing(gdb, leaseName, token); ferr != nil || !ok {
		return ferr
	}
	mergeSHA, err := mergeWithOwnerRepo(ctx, gdb, gh, &e, *ticket.PRNumber, status.HeadSHA)
	if uerr := gdb.Model(&db.IntegrationAttempt{}).
		Where("epic_id = ? AND epic_node_key = ? AND head_sha = ?",
			e.ID, ticket.EpicNodeKey, status.HeadSHA).
		Updates(mergeOutcome(mergeSHA, err)).Error; uerr != nil {
		return fmt.Errorf("epics: lane record attempt outcome: %w", uerr)
	}
	if err != nil {
		return fmt.Errorf("epics: lane merge: %w", err)
	}
	// The receipt names the merge commit: it is what the target ref
	// contains, and readiness counts it. RecordIntegration is idempotent,
	// so a reconciled second landing is harmless.
	if rerr := RecordIntegration(gdb, e.ID, ticket.EpicNodeKey, mergeSHA, targetRef); rerr != nil && !errors.Is(rerr, ErrIntegrationConflict) {
		return fmt.Errorf("epics: lane receipt: %w", rerr)
	}
	return nil
}

// reconcileAttempts resolves rows left in "merging" by a crash or a lost
// response, from the remote state — the remote is the truth about whether
// the merge landed, and the row is the truth about what we asked for.
func reconcileAttempts(ctx context.Context, gdb *gorm.DB, gh github.Client, e *db.Epic, ticket *db.Ticket) error {
	var pending []db.IntegrationAttempt
	if err := gdb.Where("epic_id = ? AND epic_node_key = ? AND state = ?",
		e.ID, ticket.EpicNodeKey, "merging").Find(&pending).Error; err != nil {
		return err
	}
	for _, att := range pending {
		st, err := getPR(ctx, gdb, gh, e, att.PRNumber)
		if err != nil {
			return fmt.Errorf("epics: lane reconcile: %w", err)
		}
		switch {
		case st.Merged:
			sha := st.MergeCommitSHA
			if sha == "" {
				sha = st.HeadSHA
			}
			if err := gdb.Model(&db.IntegrationAttempt{}).Where("id = ?", att.ID).
				Updates(map[string]any{"state": "merged", "merge_sha": sha}).Error; err != nil {
				return err
			}
			ref := st.BaseRef
			if ref == "" {
				ref = e.BaseBranch
			}
			if err := RecordIntegration(gdb, e.ID, ticket.EpicNodeKey, sha, ref); err != nil && !errors.Is(err, ErrIntegrationConflict) {
				return fmt.Errorf("epics: lane reconcile receipt: %w", err)
			}
		case st.State == "open" && st.HeadSHA == att.HeadSHA:
			// The merge call never landed (crash before or during the
			// request). The row is cleared so the fresh attempt below can
			// retry the same head.
			if err := gdb.Delete(&db.IntegrationAttempt{}, att.ID).Error; err != nil {
				return err
			}
		case st.State == "open":
			gdb.Model(&db.IntegrationAttempt{}).Where("id = ?", att.ID).Updates(map[string]any{
				"state": "failed-stale",
				"cause": "the head moved while a merge attempt was in flight",
			})
		default:
			gdb.Model(&db.IntegrationAttempt{}).Where("id = ?", att.ID).Updates(map[string]any{
				"state": "failed-unknown",
				"cause": "pull request closed without merging while a merge attempt was in flight",
			})
		}
	}
	return nil
}

// recordOnce writes a terminal attempt row, treating the unique index as
// the once-per-head bound: the second identical observation is a no-op.
func recordOnce(gdb *gorm.DB, e *db.Epic, ticket *db.Ticket, status github.PullRequestStatus, state, cause string) error {
	att := db.IntegrationAttempt{
		EpicID: e.ID, EpicNodeKey: ticket.EpicNodeKey, TicketID: ticket.ID,
		PRNumber: derefPR(ticket), HeadSHA: status.HeadSHA, BaseSHA: e.BaseSHA,
		State: state, Cause: cause,
	}
	if err := gdb.Create(&att).Error; err != nil {
		return nil // already recorded for this head
	}
	return nil
}

func derefPR(t *db.Ticket) int {
	if t.PRNumber == nil {
		return 0
	}
	return *t.PRNumber
}

func mergeWithOwnerRepo(ctx context.Context, gdb *gorm.DB, gh github.Client, e *db.Epic, number int, headSHA string) (string, error) {
	var repo db.GitHubRepo
	if err := gdb.First(&repo, "repo_remote = ?", e.RepoRemote).Error; err != nil {
		return "", fmt.Errorf("load repo %s: %w", e.RepoRemote, err)
	}
	return gh.MergePullRequest(ctx, repo.Owner, repo.Name, number, headSHA)
}

func getPR(ctx context.Context, gdb *gorm.DB, gh github.Client, e *db.Epic, number int) (github.PullRequestStatus, error) {
	var repo db.GitHubRepo
	if err := gdb.First(&repo, "repo_remote = ?", e.RepoRemote).Error; err != nil {
		return github.PullRequestStatus{}, fmt.Errorf("load repo %s: %w", e.RepoRemote, err)
	}
	return gh.GetPullRequest(ctx, repo.Owner, repo.Name, number)
}

// mergeOutcome is the attempt row update for a merge call's result.
func mergeOutcome(mergeSHA string, err error) map[string]any {
	switch {
	case err == nil:
		return map[string]any{"state": "merged", "merge_sha": mergeSHA}
	case errors.Is(err, github.ErrMergeConflictHead):
		return map[string]any{"state": "failed-stale",
			"cause": "the head moved between validation and the merge call; the new head must be validated before it can merge"}
	case errors.Is(err, github.ErrMergeForbidden):
		return map[string]any{"state": "failed-forbidden", "cause": "token permissions refused the merge"}
	default:
		return map[string]any{"state": "failed-unknown", "cause": err.Error()}
	}
}
