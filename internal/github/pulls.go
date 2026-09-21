package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gh "github.com/google/go-github/v69/github"
)

// ErrPullRequestUnprocessable wraps a 422 from the pull-request create
// endpoint. GitHub answers 422 when the request is well-formed but cannot be
// applied — most often "A pull request already exists for <owner>:<branch>",
// which is exactly what a redelivered outbox row provokes (finding I2).
//
// It is a status-code sentinel rather than a message match on purpose. A 422
// for some other reason (a base branch that does not exist, say) must not be
// mistaken for "the pull request is already there": the caller confirms that
// by looking the pull request up with FindPullRequest, and falls back to the
// ordinary retry path when there is none.
var ErrPullRequestUnprocessable = errors.New("github: pull request request was unprocessable")

// CreatePullRequest opens a pull request from head into base.
func (c *client) CreatePullRequest(ctx context.Context, owner, repo, head, base, title, body string, draft bool) (PullRequest, error) {
	pr, _, err := c.api.PullRequests.Create(ctx, owner, repo, &gh.NewPullRequest{
		Title: gh.Ptr(title),
		Head:  gh.Ptr(head),
		Base:  gh.Ptr(base),
		Body:  gh.Ptr(body),
		Draft: gh.Ptr(draft),
	})
	if err != nil {
		var resp *gh.ErrorResponse
		if errors.As(err, &resp) && resp.Response != nil &&
			resp.Response.StatusCode == http.StatusUnprocessableEntity {
			return PullRequest{}, fmt.Errorf("create pull request %s/%s %s->%s: %w: %w",
				owner, repo, head, base, ErrPullRequestUnprocessable, err)
		}
		return PullRequest{}, fmt.Errorf("create pull request %s/%s %s->%s: %w", owner, repo, head, base, err)
	}
	return PullRequest{Number: pr.GetNumber(), HTMLURL: pr.GetHTMLURL()}, nil
}

// FindPullRequest returns the most recent pull request opened from head in
// owner/repo, and whether one exists at all. State is deliberately "all":
// a redelivery has to converge on the pull request that exists even if a
// human has since closed it, because recording the wrong number — or none —
// is worse than recording a closed one.
func (c *client) FindPullRequest(ctx context.Context, owner, repo, head string) (PullRequest, bool, error) {
	prs, _, err := c.api.PullRequests.List(ctx, owner, repo, &gh.PullRequestListOptions{
		Head:        owner + ":" + head,
		State:       "all",
		Sort:        "created",
		Direction:   "desc",
		ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return PullRequest{}, false, fmt.Errorf("list pull requests %s/%s head=%s: %w", owner, repo, head, err)
	}
	if len(prs) == 0 {
		return PullRequest{}, false, nil
	}
	return PullRequest{Number: prs[0].GetNumber(), HTMLURL: prs[0].GetHTMLURL()}, true, nil
}

// PullRequestStatus is everything the monitor needs to decide whether a pull
// request is healthy, needs work, or is finished.
type PullRequestStatus struct {
	Number int
	// State is "open" or "closed". Merged distinguishes the two ways a pull
	// request closes, because they mean opposite things for the ticket: a
	// merge is the work landing, a close is it being abandoned.
	State  string
	Merged bool
	// Mergeable is nil while GitHub is still computing the merge commit.
	// That is a genuinely unknown answer and must not be read as "no
	// conflict" — the field is a pointer so the caller cannot accidentally
	// treat "not yet known" as false.
	Mergeable *bool
	// MergeableState is GitHub's own summary: clean, dirty (conflicts),
	// blocked (a required review or check), unstable (a non-required check
	// failed), behind, or unknown.
	MergeableState string
	HeadSHA        string
	BaseRef        string
}

// Conflicted reports whether the pull request cannot merge because the branch
// conflicts with its base — as opposed to any of the other reasons a pull
// request may be unmergeable, such as an outstanding review.
//
// Both signals must agree. mergeable_state alone is not enough: it reads
// "unknown" while GitHub computes, and briefly after a push, and acting on
// that would send a ticket back to revising to resolve a conflict that does
// not exist.
func (s PullRequestStatus) Conflicted() bool {
	return s.Mergeable != nil && !*s.Mergeable && s.MergeableState == "dirty"
}

// CheckFailure is one failing check on a pull request's head commit.
type CheckFailure struct {
	Name       string
	Conclusion string
	DetailsURL string
	// Summary is the check's own output summary where it has one. It is the
	// only part of a failure the agent can act on without fetching logs, so
	// it is carried even though it is often empty.
	Summary string
	// NeedsHuman marks a failure that no commit can clear, so attempting a
	// fix is guaranteed to fail and only spends an agent run and a push.
	NeedsHuman bool
}

// GetPullRequest returns the current state of a pull request.
func (c *client) GetPullRequest(ctx context.Context, owner, repo string, number int) (PullRequestStatus, error) {
	pr, _, err := c.api.PullRequests.Get(ctx, owner, repo, number)
	if err != nil {
		return PullRequestStatus{}, fmt.Errorf("get pull request %s/%s#%d: %w", owner, repo, number, err)
	}
	st := PullRequestStatus{
		Number:         pr.GetNumber(),
		State:          pr.GetState(),
		Merged:         pr.GetMerged(),
		MergeableState: pr.GetMergeableState(),
		HeadSHA:        pr.GetHead().GetSHA(),
		BaseRef:        pr.GetBase().GetRef(),
	}
	if pr.Mergeable != nil {
		st.Mergeable = gh.Ptr(pr.GetMergeable())
	}
	return st, nil
}

// ListFailedChecks returns the checks on ref that finished unsuccessfully.
//
// Both of GitHub's two check systems are consulted, because a repository can
// use either and most use both: check runs (GitHub Actions and most Apps)
// and the older commit statuses (many external CI services). Reading only
// one would report a pull request as green while the other system showed it
// red.
//
// Only terminal, unsuccessful conclusions are returned. A check still
// running is not a failure, and neither is one that was skipped or that
// GitHub reports as neutral — treating those as failures would send the
// ticket back to revising on every push, before CI had even finished.
func (c *client) ListFailedChecks(ctx context.Context, owner, repo, ref string) ([]CheckFailure, error) {
	var out []CheckFailure

	opt := &gh.ListCheckRunsOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		runs, resp, err := c.api.Checks.ListCheckRunsForRef(ctx, owner, repo, ref, opt)
		if err != nil {
			return nil, fmt.Errorf("list check runs for %s/%s@%s: %w", owner, repo, ref, err)
		}
		for _, r := range runs.CheckRuns {
			if r.GetStatus() != "completed" || !failedConclusion(r.GetConclusion()) {
				continue
			}
			out = append(out, CheckFailure{
				Name:       r.GetName(),
				Conclusion: r.GetConclusion(),
				DetailsURL: r.GetDetailsURL(),
				Summary:    r.GetOutput().GetSummary(),
				NeedsHuman: needsHumanConclusion(r.GetConclusion()),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opt.Page = resp.NextPage
	}

	sopt := &gh.ListOptions{PerPage: 100}
	for {
		statuses, resp, err := c.api.Repositories.ListStatuses(ctx, owner, repo, ref, sopt)
		if err != nil {
			return nil, fmt.Errorf("list statuses for %s/%s@%s: %w", owner, repo, ref, err)
		}
		// ListStatuses returns every status ever posted for the ref, newest
		// first, so a context that failed and was then re-run green appears
		// twice. Only the newest entry per context is current.
		seen := make(map[string]bool, len(statuses))
		for _, s := range statuses {
			ctxName := s.GetContext()
			if seen[ctxName] {
				continue
			}
			seen[ctxName] = true
			if s.GetState() != "failure" && s.GetState() != "error" {
				continue
			}
			out = append(out, CheckFailure{
				Name:       ctxName,
				Conclusion: s.GetState(),
				DetailsURL: s.GetTargetURL(),
				Summary:    s.GetDescription(),
			})
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		sopt.Page = resp.NextPage
	}
	return out, nil
}

// failedConclusion reports whether a completed check run's conclusion is one
// a human would call a failure.
func failedConclusion(c string) bool {
	switch c {
	case "failure", "timed_out", "action_required", "startup_failure":
		return true
	default:
		// success, neutral, skipped, cancelled. cancelled is deliberately
		// not a failure: it is what the workflow's own
		// cancel-in-progress does to the previous run on every new push,
		// so treating it as one would flag a ticket for the act of
		// pushing a fix.
		return false
	}
}

// needsHumanConclusion reports whether a failure is one no commit can clear.
//
// action_required is GitHub holding a workflow for maintainer approval —
// what a fork's pull request gets when the repository requires approval for
// outside contributors. Nothing in the tree causes it and no push clears it;
// a person with write access clicking "Approve and run" does. Observed on
// this project's own pull request, where every automatic fix attempt would
// have produced another run held for the same approval.
func needsHumanConclusion(c string) bool {
	return c == "action_required"
}
