package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gh "github.com/google/go-github/v69/github"
)

// AddLabel adds a single label to an issue, leaving existing labels intact.
func (c *client) AddLabel(ctx context.Context, owner, repo string, number int, label string) error {
	_, _, err := c.api.Issues.AddLabelsToIssue(ctx, owner, repo, number, []string{label})
	if err != nil {
		return fmt.Errorf("add label %q to %s/%s#%d: %w", label, owner, repo, number, err)
	}
	return nil
}

// RemoveLabel removes a single label from an issue. Removing a label that is
// not present is not an error.
func (c *client) RemoveLabel(ctx context.Context, owner, repo string, number int, label string) error {
	_, err := c.api.Issues.RemoveLabelForIssue(ctx, owner, repo, number, label)
	if err != nil {
		var errResp *gh.ErrorResponse
		if errors.As(err, &errResp) && errResp.Response != nil &&
			errResp.Response.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("remove label %q from %s/%s#%d: %w", label, owner, repo, number, err)
	}
	return nil
}
