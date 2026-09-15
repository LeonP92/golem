package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	gh "github.com/google/go-github/v69/github"
)

// ListIssuesSince returns issues carrying label that changed at or after
// since, including closed ones. Passing a non-empty etag makes the request
// conditional via If-None-Match; a 304 response returns
// IssuePage{NotModified: true} and Issues is empty.
//
// This bypasses the Issues.ListByRepo convenience method because go-github
// gives no way to attach a request header through it: the request is built
// by hand here (mirroring the query string ListByRepo would have produced)
// so If-None-Match can be set before the request is sent.
func (c *client) ListIssuesSince(ctx context.Context, owner, repo, label string, since time.Time, etag string) (IssuePage, error) {
	path := fmt.Sprintf("repos/%s/%s/issues", owner, repo)

	var page IssuePage
	haveETag := false
	pageNum := 0
	for {
		q := url.Values{}
		q.Set("state", "all")
		if label != "" {
			q.Set("labels", label)
		}
		q.Set("since", since.Format(time.RFC3339))
		q.Set("per_page", "100")
		if pageNum != 0 {
			q.Set("page", strconv.Itoa(pageNum))
		}

		req, err := c.api.NewRequest(http.MethodGet, path+"?"+q.Encode(), nil)
		if err != nil {
			return IssuePage{}, fmt.Errorf("list issues for %s/%s: %w", owner, repo, err)
		}
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}

		var issues []*gh.Issue
		resp, err := c.api.Do(ctx, req, &issues)
		if err != nil {
			var errResp *gh.ErrorResponse
			if errors.As(err, &errResp) && errResp.Response != nil &&
				errResp.Response.StatusCode == http.StatusNotModified {
				return IssuePage{ETag: etag, NotModified: true}, nil
			}
			return IssuePage{}, fmt.Errorf("list issues for %s/%s: %w", owner, repo, err)
		}

		// A conditional GET must be re-sent with the ETag of the *first*
		// page, since that is what identifies the whole listing; capture it
		// once and ignore ETags on any subsequent pages.
		if !haveETag {
			page.ETag = resp.Header.Get("ETag")
			haveETag = true
		}

		for _, in := range issues {
			// Pull requests come back from the issues endpoint too; skip them.
			if in.IsPullRequest() {
				continue
			}
			page.Issues = append(page.Issues, toIssue(in))
		}
		if resp.NextPage == 0 {
			return page, nil
		}
		pageNum = resp.NextPage
	}
}

// GetIssue fetches a single issue by number.
func (c *client) GetIssue(ctx context.Context, owner, repo string, number int) (Issue, error) {
	in, _, err := c.api.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return Issue{}, fmt.Errorf("get issue %s/%s#%d: %w", owner, repo, number, err)
	}
	return toIssue(in), nil
}

// DefaultBranch returns the repository's default branch name.
func (c *client) DefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, _, err := c.api.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return "", fmt.Errorf("get default branch for %s/%s: %w", owner, repo, err)
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
