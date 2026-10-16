package epics

import (
	"context"
	"errors"
	"fmt"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// MergeFinal merges the assembled branch's final pull request to base.
// Every guard the delivery target implies is checked here, at the edge,
// because the UI button and any future automation both arrive at this
// function:
//
//   - The run must be awaiting final review: all children integrated.
//   - A feature-branch delivery target never touches base — that target
//     is defined by the final merge being the human's own.
//   - AutoMergeFinal must be on, which creation only allows for the
//     base-branch target, and which forces the tester gate on.
//   - A current tester pass: a verdict for the final candidate (empty
//     node key) whose head SHA matches the final PR's head. Fail,
//     inconclusive, missing, or stale verdicts all block.
//
// The merge itself is pinned to the validated head, and the attempt is
// recorded with the same evidence rules as the child lane.
func MergeFinal(ctx context.Context, gdb *gorm.DB, gh github.Client, epicID string) error {
	var e db.Epic
	if err := gdb.First(&e, "id = ?", epicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	if e.State == "completed" {
		return nil // merging twice is one merge
	}
	if e.State != "awaiting-final-review" {
		return fmt.Errorf("epics: final merge needs awaiting-final-review, state is %q", e.State)
	}
	if e.DeliveryTarget != TargetBaseBranch {
		return fmt.Errorf("epics: the feature-branch delivery target never touches base; the final merge is yours")
	}
	if !e.AutoMergeFinal {
		return fmt.Errorf("epics: final auto-merge is off for this run")
	}

	var repo db.GitHubRepo
	if err := gdb.First(&repo, "repo_remote = ?", e.RepoRemote).Error; err != nil {
		return fmt.Errorf("epics: load repo %s: %w", e.RepoRemote, err)
	}
	pr, found, err := gh.FindPullRequest(ctx, repo.Owner, repo.Name, e.IntegrationRef)
	if err != nil {
		return fmt.Errorf("epics: find final pull request: %w", err)
	}
	if !found {
		return fmt.Errorf("epics: no final pull request from %s exists yet", e.IntegrationRef)
	}
	status, err := gh.GetPullRequest(ctx, repo.Owner, repo.Name, pr.Number)
	if err != nil {
		return fmt.Errorf("epics: read final pull request: %w", err)
	}
	if status.State == "closed" && status.Merged {
		// The final PR is already merged — by a retry of ours or by a
		// human. Either way the run is delivered; converge the record.
		return markCompleted(gdb, e.ID)
	}
	if status.State != "open" {
		return fmt.Errorf("epics: the final pull request is closed without merging")
	}
	if status.BaseRef != "" && status.BaseRef != e.BaseBranch {
		return fmt.Errorf("epics: the final pull request targets %s, not base %s — refusing", status.BaseRef, e.BaseBranch)
	}

	// The tester gate: only a pass verdict recorded for this exact final
	// candidate head authorizes the merge. The final candidate is the
	// integration branch itself, so the verdict's node key is empty.
	if v := latestVerdict(gdb, e.ID, "", status.HeadSHA); v == nil {
		return fmt.Errorf("epics: final auto-merge needs a tester pass for head %s; none recorded", status.HeadSHA)
	} else if v.Verdict != "pass" {
		return fmt.Errorf("epics: tester verdict for the final candidate is %q: %s", v.Verdict, v.Evidence)
	}

	// A transient failure on this head may retry; any other row is final
	// and is reported, never swallowed.
	reopened := gdb.Model(&db.IntegrationAttempt{}).
		Where("epic_id = ? AND epic_node_key = '' AND head_sha = ? AND state = ?", e.ID, status.HeadSHA, "failed-unknown").
		Updates(map[string]any{"state": "merging", "cause": "", "merge_sha": ""})
	if reopened.Error != nil {
		return reopened.Error
	}
	if reopened.RowsAffected == 0 {
		att := db.IntegrationAttempt{
			EpicID: e.ID, EpicNodeKey: "", TicketID: "final", PRNumber: pr.Number,
			HeadSHA: status.HeadSHA, BaseSHA: e.BaseSHA, State: "merging",
		}
		if err := gdb.Create(&att).Error; err != nil {
			var prior db.IntegrationAttempt
			if gdb.First(&prior, "epic_id = ? AND epic_node_key = '' AND head_sha = ?", e.ID, status.HeadSHA).Error == nil {
				return fmt.Errorf("epics: final merge of head %s is already %s: %s", status.HeadSHA, prior.State, prior.Cause)
			}
			return fmt.Errorf("epics: record final attempt: %w", err)
		}
	}
	mergeSHA, err := gh.MergePullRequest(ctx, repo.Owner, repo.Name, pr.Number, status.HeadSHA)
	if uerr := gdb.Model(&db.IntegrationAttempt{}).
		Where("epic_id = ? AND epic_node_key = '' AND head_sha = ?", e.ID, status.HeadSHA).
		Updates(mergeOutcome(mergeSHA, err)).Error; uerr != nil {
		return fmt.Errorf("epics: record final attempt outcome: %w", uerr)
	}
	if err != nil {
		return fmt.Errorf("epics: final merge: %w", err)
	}
	return markCompleted(gdb, e.ID)
}

func markCompleted(gdb *gorm.DB, epicID string) error {
	return gdb.Model(&db.Epic{}).Where("id = ?", epicID).Update("state", "completed").Error
}
