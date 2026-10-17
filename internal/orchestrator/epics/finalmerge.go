package epics

import (
	"context"
	"fmt"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// MergeFinal merges the integration branch's final pull request to base.
// Only for a base-branch delivery with final auto-merge on, after every
// child integrated, and with a tester pass for the final PR's exact head.
func MergeFinal(ctx context.Context, gdb *gorm.DB, gh github.Client, epicID string) error {
	e, err := load(gdb, epicID)
	if err != nil {
		return err
	}
	switch {
	case e.State == StateCompleted:
		return nil
	case e.State != StateAwaitingFinalReview:
		return fmt.Errorf("%w: final merge needs %s, run is %s", ErrWrongState, StateAwaitingFinalReview, e.State)
	case e.DeliveryTarget != TargetBaseBranch || !e.AutoMergeFinal:
		return fmt.Errorf("%w: final auto-merge is off for this run; the final merge is yours", ErrWrongState)
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
		return fmt.Errorf("%w: no final pull request from %s exists yet", ErrWrongState, e.IntegrationRef)
	}
	st, err := gh.GetPullRequest(ctx, repo.Owner, repo.Name, pr.Number)
	if err != nil {
		return fmt.Errorf("epics: read final pull request: %w", err)
	}
	switch {
	case st.Merged:
		return markCompleted(gdb, e.ID)
	case st.State != "open":
		return fmt.Errorf("%w: the final pull request is closed without merging", ErrWrongState)
	case st.BaseRef != "" && st.BaseRef != e.BaseBranch:
		return fmt.Errorf("%w: the final pull request targets %s, not %s", ErrWrongState, st.BaseRef, e.BaseBranch)
	}
	if v := LatestVerdict(gdb, e.ID, "", st.HeadSHA); v == nil {
		return fmt.Errorf("%w: final auto-merge needs a tester pass for head %s", ErrWrongState, st.HeadSHA)
	} else if v.Verdict != "pass" {
		return fmt.Errorf("%w: tester verdict for head %s is %s: %s", ErrWrongState, st.HeadSHA, v.Verdict, v.Evidence)
	}
	if _, err := gh.MergePullRequest(ctx, repo.Owner, repo.Name, pr.Number, st.HeadSHA); err != nil {
		return fmt.Errorf("epics: final merge: %w", err)
	}
	return markCompleted(gdb, e.ID)
}

func markCompleted(gdb *gorm.DB, epicID string) error {
	return gdb.Model(&db.Epic{}).Where("id = ?", epicID).Update("state", StateCompleted).Error
}
