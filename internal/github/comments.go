package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v69/github"
)

// CreateComment posts a comment on an issue.
func (c *client) CreateComment(ctx context.Context, owner, repo string, number int, body string) error {
	_, _, err := c.api.Issues.CreateComment(ctx, owner, repo, number,
		&gh.IssueComment{Body: gh.Ptr(body)})
	if err != nil {
		return fmt.Errorf("create comment on %s/%s#%d: %w", owner, repo, number, err)
	}
	return nil
}
