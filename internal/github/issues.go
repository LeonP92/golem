package github

import (
	"context"
	"errors"
	"net/http"
	"time"

	gh "github.com/google/go-github/v69/github"
)

// ListIssuesSince returns issues carrying label that changed at or after
// since, including closed ones. Passing a non-empty etag makes the request
// conditional; a 304 response returns IssuePage{NotModified: true}.
func (c *client) ListIssuesSince(ctx context.Context, owner, repo, label string, since time.Time, etag string) (IssuePage, error) {
	opts := &gh.IssueListByRepoOptions{
		State:       "all",
		Since:       since,
		ListOptions: gh.ListOptions{PerPage: 100},
	}
	if label != "" {
		opts.Labels = []string{label}
	}

	var page IssuePage
	for {
		issues, resp, err := c.api.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			var errResp *gh.ErrorResponse
			if errors.As(err, &errResp) && errResp.Response != nil &&
				errResp.Response.StatusCode == http.StatusNotModified {
				return IssuePage{ETag: etag, NotModified: true}, nil
			}
			return IssuePage{}, err
		}
		if resp != nil && resp.StatusCode == http.StatusNotModified {
			return IssuePage{ETag: etag, NotModified: true}, nil
		}
		for _, in := range issues {
			// Pull requests come back from the issues endpoint too; skip them.
			if in.IsPullRequest() {
				continue
			}
			page.Issues = append(page.Issues, toIssue(in))
		}
		if resp == nil || resp.NextPage == 0 {
			if resp != nil {
				page.ETag = resp.Header.Get("ETag")
			}
			return page, nil
		}
		opts.Page = resp.NextPage
	}
}

// GetIssue fetches a single issue by number.
func (c *client) GetIssue(ctx context.Context, owner, repo string, number int) (Issue, error) {
	in, _, err := c.api.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return Issue{}, err
	}
	return toIssue(in), nil
}

// DefaultBranch returns the repository's default branch name.
func (c *client) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, _, err := c.api.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return r.GetDefaultBranch(), nil
}

// Task 2 replaces each of the following stubs with a real implementation.

func (c *client) CreateIssue(context.Context, string, string, string, string, []string) (Issue, error) {
	return Issue{}, errors.New("not implemented")
}

func (c *client) SetIssueState(context.Context, string, string, int, string) error {
	return errors.New("not implemented")
}

func (c *client) AddLabel(context.Context, string, string, int, string) error {
	return errors.New("not implemented")
}

func (c *client) RemoveLabel(context.Context, string, string, int, string) error {
	return errors.New("not implemented")
}

func (c *client) CreateComment(context.Context, string, string, int, string) error {
	return errors.New("not implemented")
}

func (c *client) CreatePullRequest(context.Context, string, string, string, string, string, string, bool) (PullRequest, error) {
	return PullRequest{}, errors.New("not implemented")
}
