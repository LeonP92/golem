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
