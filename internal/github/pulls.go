package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v69/github"
)

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
		return PullRequest{}, fmt.Errorf("create pull request %s/%s %s->%s: %w", owner, repo, head, base, err)
	}
	return PullRequest{Number: pr.GetNumber(), HTMLURL: pr.GetHTMLURL()}, nil
}
