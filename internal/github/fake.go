package github

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Fake is an in-memory Client for tests. It is safe for concurrent use.
type Fake struct {
	mu sync.Mutex

	Issues   map[int]Issue
	Comments map[int][]string
	PRs      []PullRequest
	Default  string // default branch returned by DefaultBranch

	// ETag, when non-empty, simulates GitHub's conditional-request caching:
	// if ListIssuesSince is called with an etag argument equal to ETag, it
	// returns IssuePage{ETag: ETag, NotModified: true} with no issues,
	// mirroring a real 304 response. This does not model real HTTP caching
	// semantics (e.g. ETag is never itself changed by adding issues) — it is
	// a deliberately simple lever for tests that need to exercise a
	// caller's NotModified handling.
	ETag string

	// FailNext, when non-nil, is returned by the next call to any method and
	// then cleared — for exercising retry paths.
	FailNext error

	// Calls records method names in order, for asserting what was and was not
	// called.
	Calls []string
}

// NewFake returns an empty Fake with a "main" default branch.
func NewFake() *Fake {
	return &Fake{
		Issues:   map[int]Issue{},
		Comments: map[int][]string{},
		Default:  "main",
	}
}

// AddIssue seeds an issue into the fake.
func (f *Fake) AddIssue(i Issue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Issues[i.Number] = i
}

// record notes the call and consumes FailNext if set.
func (f *Fake) record(name string) error {
	f.Calls = append(f.Calls, name)
	if f.FailNext != nil {
		err := f.FailNext
		f.FailNext = nil
		return err
	}
	return nil
}

func (f *Fake) ListIssuesSince(_ context.Context, _, _, label string, since time.Time, etag string) (IssuePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ListIssuesSince"); err != nil {
		return IssuePage{}, err
	}
	if f.ETag != "" && etag == f.ETag {
		return IssuePage{ETag: f.ETag, NotModified: true}, nil
	}
	var page IssuePage
	for _, i := range f.Issues {
		if label != "" && !i.HasLabel(label) {
			continue
		}
		if i.UpdatedAt.Before(since) {
			continue
		}
		page.Issues = append(page.Issues, i)
	}
	return page, nil
}

func (f *Fake) GetIssue(_ context.Context, _, _ string, number int) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("GetIssue"); err != nil {
		return Issue{}, err
	}
	i, ok := f.Issues[number]
	if !ok {
		return Issue{}, fmt.Errorf("issue %d not found", number)
	}
	return i, nil
}

func (f *Fake) CreateIssue(_ context.Context, _, _, title, body string, labels []string) (Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreateIssue"); err != nil {
		return Issue{}, err
	}
	n := len(f.Issues) + 1
	i := Issue{Number: n, Title: title, Body: body, State: "open",
		Labels: labels, UpdatedAt: time.Now()}
	f.Issues[n] = i
	return i, nil
}

func (f *Fake) SetIssueState(_ context.Context, _, _ string, number int, state string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("SetIssueState"); err != nil {
		return err
	}
	i, ok := f.Issues[number]
	if !ok {
		return fmt.Errorf("issue %d not found", number)
	}
	i.State = state
	f.Issues[number] = i
	return nil
}

func (f *Fake) AddLabel(_ context.Context, _, _ string, number int, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("AddLabel"); err != nil {
		return err
	}
	i, ok := f.Issues[number]
	if !ok {
		return fmt.Errorf("issue %d not found", number)
	}
	if !i.HasLabel(label) {
		i.Labels = append(append([]string{}, i.Labels...), label)
		f.Issues[number] = i
	}
	return nil
}

func (f *Fake) RemoveLabel(_ context.Context, _, _ string, number int, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("RemoveLabel"); err != nil {
		return err
	}
	i, ok := f.Issues[number]
	if !ok {
		return nil
	}
	kept := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		if l != label {
			kept = append(kept, l)
		}
	}
	i.Labels = kept
	f.Issues[number] = i
	return nil
}

func (f *Fake) CreateComment(_ context.Context, _, _ string, number int, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreateComment"); err != nil {
		return err
	}
	f.Comments[number] = append(f.Comments[number], body)
	return nil
}

func (f *Fake) CreatePullRequest(_ context.Context, _, _, _, _, _, _ string, _ bool) (PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreatePullRequest"); err != nil {
		return PullRequest{}, err
	}
	pr := PullRequest{Number: 100 + len(f.PRs), HTMLURL: "https://example.test/pull"}
	f.PRs = append(f.PRs, pr)
	return pr, nil
}

func (f *Fake) DefaultBranch(_ context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("DefaultBranch"); err != nil {
		return "", err
	}
	return f.Default, nil
}
