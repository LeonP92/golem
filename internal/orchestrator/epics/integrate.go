package epics

import (
	"context"
	"errors"
	"fmt"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// MergeChild is the integration lane for one epic child whose pull request
// the PR monitor just saw with no failing checks. It merges when the run
// allows it and every gate holds, pinned to the validated head. Instead of
// keeping its own state it returns what the monitor should do through its
// existing paths: fix (back to revising with feedback) or park
// (needs-attention with a cause). Both empty means merged or wait.
func MergeChild(ctx context.Context, gdb *gorm.DB, gh github.Client, owner, name string,
	t *db.Ticket, st github.PullRequestStatus) (fix, park string, err error) {
	if t.EpicID == nil || t.PRNumber == nil {
		return "", "", nil
	}
	e, err := load(gdb, *t.EpicID)
	if err != nil {
		return "", "", err
	}
	if !e.AutoMergeChildren || e.State != StateRunning {
		return "", "", nil
	}
	// Pending CI, branch protection or uncomputed mergeability: wait.
	if st.Mergeable == nil || !*st.Mergeable || st.MergeableState != "clean" {
		return "", "", nil
	}
	if target := TargetRef(e); st.BaseRef != "" && st.BaseRef != target {
		return "", fmt.Sprintf("Pull request #%d targets %s, but this epic integrates into %s; not merging.",
			st.Number, st.BaseRef, target), nil
	}
	if e.TesterRequired {
		v := LatestVerdict(gdb, e.ID, t.EpicNodeKey, st.HeadSHA)
		switch {
		case v == nil:
			return "", "", nil // the tester runs after each push
		case v.Verdict == "fail":
			return fmt.Sprintf("The tester failed head %s.\n\nScenarios: %s\nEvidence: %s\n\n"+
				"Fix the cause on the existing branch.", st.HeadSHA, v.Scenarios, v.Evidence), "", nil
		case v.Verdict != "pass":
			return "", fmt.Sprintf("The tester could not reach a verdict for head %s (%s): %s",
				st.HeadSHA, v.Verdict, v.Evidence), nil
		}
	}
	sha, err := gh.MergePullRequest(ctx, owner, name, *t.PRNumber, st.HeadSHA)
	switch {
	case errors.Is(err, github.ErrMergeConflictHead):
		return "", "", nil // head moved; the next pass validates the new one
	case errors.Is(err, github.ErrMergeForbidden):
		return "", "Auto-merge was refused by GitHub (token permissions or branch protection): " + err.Error(), nil
	case err != nil:
		return "", "", fmt.Errorf("merge pull request #%d: %w", *t.PRNumber, err)
	}
	return "", "", RecordIntegration(gdb, e.ID, t.EpicNodeKey, sha)
}

// RecordVerdict stores a tester verdict (append-only).
func RecordVerdict(gdb *gorm.DB, v db.TesterVerdict) error {
	if v.EpicID == "" || v.HeadSHA == "" {
		return fmt.Errorf("%w: a verdict needs its epic and the tested head", ErrInvalid)
	}
	switch v.Verdict {
	case "pass", "fail", "inconclusive":
	default:
		return fmt.Errorf("%w: unknown verdict %q", ErrInvalid, v.Verdict)
	}
	return gdb.Create(&v).Error
}

// LatestVerdict returns the newest verdict for this exact head, or nil: a
// verdict for any other head describes code the candidate no longer is.
func LatestVerdict(gdb *gorm.DB, epicID, nodeKey, headSHA string) *db.TesterVerdict {
	var vs []db.TesterVerdict
	gdb.Where("epic_id = ? AND epic_node_key = ? AND head_sha = ?", epicID, nodeKey, headSHA).
		Order("id desc").Limit(1).Find(&vs)
	if len(vs) == 0 {
		return nil
	}
	return &vs[0]
}
