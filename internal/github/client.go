// Package github wraps the GitHub REST API with the narrow surface Golem
// needs. All access goes through Client so the sync engine can be tested
// against an in-memory fake with no network.
package github

import (
	"context"
	"net/http"
	"net/url"
	"time"

	gh "github.com/google/go-github/v69/github"
)

// Issue is Golem's projection of a GitHub issue — only the fields the sync
// engine reads.
type Issue struct {
	Number    int
	Title     string
	Body      string
	State     string // open | closed
	HTMLURL   string
	UpdatedAt time.Time
	Labels    []string
}

// HasLabel reports whether the issue carries the named label.
func (i Issue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if l == name {
			return true
		}
	}
	return false
}

// IssuePage is one response from ListIssuesSince. NotModified is true when
// GitHub answered 304 to a conditional request, in which case Issues is empty
// and the caller should keep its existing state.
type IssuePage struct {
	Issues      []Issue
	ETag        string
	NotModified bool
}

// PullRequest is Golem's projection of a created pull request.
type PullRequest struct {
	Number  int
	HTMLURL string
}

// Client is the GitHub surface Golem depends on.
type Client interface {
	ListIssuesSince(ctx context.Context, owner, repo, label string, since time.Time, etag string) (IssuePage, error)
	GetIssue(ctx context.Context, owner, repo string, number int) (Issue, error)
	CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (Issue, error)
	SetIssueState(ctx context.Context, owner, repo string, number int, state string) error
	AddLabel(ctx context.Context, owner, repo string, number int, label string) error
	RemoveLabel(ctx context.Context, owner, repo string, number int, label string) error
	CreateComment(ctx context.Context, owner, repo string, number int, body string) error
	CreatePullRequest(ctx context.Context, owner, repo, head, base, title, body string, draft bool) (PullRequest, error)
	DefaultBranch(ctx context.Context, owner, repo string) (string, error)
}

type client struct {
	api *gh.Client
}

// New returns a Client authenticated with token. apiBase is empty for
// github.com, or a GitHub Enterprise base URL (with trailing slash).
//
// apiBase is assigned directly to the underlying client's BaseURL/UploadURL
// rather than via WithEnterpriseURLs, which appends "/api/v3/" to the path
// when the host has no "api." prefix — that rewrite is wrong for test servers
// and for Enterprise operators who already include "/api/v3/" in apiBase.
// GitHub Enterprise operators must therefore supply the full base URL
// including the "/api/v3/" suffix.
func New(token, apiBase string) (Client, error) {
	api := gh.NewClient(&http.Client{Timeout: 30 * time.Second}).WithAuthToken(token)
	if apiBase != "" {
		base, err := url.Parse(apiBase)
		if err != nil {
			return nil, err
		}
		api.BaseURL = base
		api.UploadURL = base
	}
	return &client{api: api}, nil
}

// toIssue converts a go-github issue into Golem's projection.
func toIssue(in *gh.Issue) Issue {
	out := Issue{
		Number:  in.GetNumber(),
		Title:   in.GetTitle(),
		Body:    in.GetBody(),
		State:   in.GetState(),
		HTMLURL: in.GetHTMLURL(),
	}
	if in.UpdatedAt != nil {
		out.UpdatedAt = in.UpdatedAt.Time
	}
	for _, l := range in.Labels {
		out.Labels = append(out.Labels, l.GetName())
	}
	return out
}
