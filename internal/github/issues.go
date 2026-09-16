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
		// Only the first page is conditional. The stored ETag identifies
		// the listing as a whole, so sending it on page 2 asks a question
		// it cannot answer meaningfully — and if page 2 ever did answer
		// 304, the branch below would discard page 1's already-collected
		// issues and report "nothing changed", while IngestRepo's 304 path
		// advances neither the cursor nor the ETag, so the next poll would
		// repeat it identically. That is a permanent stall, not a one-off
		// loss.
		if etag != "" && pageNum == 0 {
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

// CreateIssue opens a new issue with the given labels.
func (c *client) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (Issue, error) {
	in, _, err := c.api.Issues.Create(ctx, owner, repo, &gh.IssueRequest{
		Title:  gh.Ptr(title),
		Body:   gh.Ptr(body),
		Labels: &labels,
	})
	if err != nil {
		return Issue{}, fmt.Errorf("create issue %s/%s: %w", owner, repo, err)
	}
	return toIssue(in), nil
}

// SetIssueState sets an issue to "open" or "closed".
func (c *client) SetIssueState(ctx context.Context, owner, repo string, number int, state string) error {
	_, _, err := c.api.Issues.Edit(ctx, owner, repo, number, &gh.IssueRequest{
		State: gh.Ptr(state),
	})
	if err != nil {
		return fmt.Errorf("set issue state %s/%s#%d: %w", owner, repo, number, err)
	}
	return nil
}
