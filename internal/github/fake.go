package github

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Fake is an in-memory Client for tests. Its methods are safe for concurrent
// use — each takes the internal lock. Reading its exported fields (Comments,
// PRs, Calls, Issues, ETag, FailNext) directly is safe only when nothing else
// can be calling a Fake method concurrently, which holds for every
// synchronous test. A caller that inspects or configures state from a
// separate goroutine while a worker may be calling into the fake in the
// background — as the ghsync worker's ingest and drain loops do — must use
// the accessor methods instead (CommentsFor, CallsSnapshot, PRsSnapshot,
// IssueByNumber, SetFailNext), or race with the lock every method already
// takes.
type Fake struct {
	mu sync.Mutex

	Issues   map[int]Issue
	Comments map[int][]string
	PRs      []PullRequest
	Default  string // default branch returned by DefaultBranch

	// PRStatuses and FailedChecks drive the pull-request monitor. Both are
	// keyed so a test can set up one unhealthy pull request without having
	// to describe every other one: an absent entry means healthy.
	PRStatuses   map[int]PullRequestStatus
	FailedChecks map[string][]CheckFailure

	// prByHead indexes PRs by head branch, so CreatePullRequest can refuse a
	// second pull request from the same branch the way GitHub does (422) and
	// FindPullRequest can answer for it. Without this the fake accepted
	// every redelivery and quietly created duplicates, which is how finding
	// I2 stayed invisible to the suite.
	prByHead map[string]PullRequest

	// ETag, when non-empty, simulates GitHub's conditional-request caching:
	// if ListIssuesSince is called with an etag argument equal to ETag, it
	// returns IssuePage{ETag: ETag, NotModified: true} with no issues,
	// mirroring a real 304 response. This does not model real HTTP caching
	// semantics (e.g. ETag is never itself changed by adding issues) — it is
	// a deliberately simple lever for tests that need to exercise a
	// caller's NotModified handling.
	ETag string

	// PageETag, when non-empty, is the ETag returned on a normal (non-304)
	// page. It is separate from ETag so a test can distinguish "the ETag
	// GitHub is offering now" from "the ETag that provokes a 304", which is
	// what the partial-failure guard needs: a page that fails partway must
	// not store a fresh ETag, or the next poll answers 304 and the issue
	// that failed is never retried.
	PageETag string

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
		prByHead: map[string]PullRequest{},
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
	// A zero `since` is legal at this interface and means "everything": the
	// UpdatedAt filter below lets every issue through, which is what a first
	// poll wants. It is the WIRE format that GitHub is strict about — a zero
	// time.Time formatted as 0001-01-01T00:00:00Z draws a 422 — and the real
	// client answers that by omitting the parameter rather than by refusing
	// the argument. Rejecting it here would make this fake stricter than the
	// thing it stands in for, and would have failed every caller that quite
	// correctly asks for the full history.
	//
	// That distinction is pinned where it actually lives, against an HTTP
	// server: TestListIssuesSinceOmitsAZeroCursor in pagination_test.go.
	if f.ETag != "" && etag == f.ETag {
		return IssuePage{ETag: f.ETag, NotModified: true}, nil
	}
	page := IssuePage{ETag: f.PageETag}
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
	// Defensive copy: without it this returns the live Issues[number] entry's
	// Labels slice header, so a caller mutating the returned Issue's Labels
	// races with any concurrent method call that also touches this issue —
	// the exact defect IssueByNumber (below) was added to avoid.
	i.Labels = append([]string(nil), i.Labels...)
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

// CommentsFor returns a copy of the comments recorded for the given issue
// number. Reading the Comments field directly is safe only when nothing else
// can be calling CreateComment concurrently (true for every synchronous
// test); a caller that polls Comments from a separate goroutine while a
// worker may be delivering outbox rows in the background — as the ghsync
// worker's drain loop does — must use this method instead, or race with the
// lock CreateComment already takes.
func (f *Fake) CommentsFor(number int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Comments[number]))
	copy(out, f.Comments[number])
	return out
}

// CreatePullRequest mirrors GitHub's behaviour for a branch that already has
// a pull request: it refuses with ErrPullRequestUnprocessable rather than
// opening a second one. That is the 422 a redelivered outbox row provokes,
// and modelling it here is what lets the suite see finding I2 at all.
func (f *Fake) CreatePullRequest(_ context.Context, _, _, head, _, _, _ string, _ bool) (PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreatePullRequest"); err != nil {
		return PullRequest{}, err
	}
	if existing, ok := f.prByHead[head]; ok {
		return PullRequest{}, fmt.Errorf(
			"%w: A pull request already exists for %s (#%d)",
			ErrPullRequestUnprocessable, head, existing.Number)
	}
	pr := PullRequest{Number: 100 + len(f.PRs), HTMLURL: "https://example.test/pull"}
	f.PRs = append(f.PRs, pr)
	f.prByHead[head] = pr
	return pr, nil
}

func (f *Fake) FindPullRequest(_ context.Context, _, _, head string) (PullRequest, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("FindPullRequest"); err != nil {
		return PullRequest{}, false, err
	}
	pr, ok := f.prByHead[head]
	return pr, ok, nil
}

func (f *Fake) DefaultBranch(_ context.Context, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("DefaultBranch"); err != nil {
		return "", err
	}
	return f.Default, nil
}

// PRStatus is what GetPullRequest returns, keyed by pull request number.
// A number with no entry answers as an open, clean pull request, so a test
// that does not care about the monitor gets the healthy case for free.
func (f *Fake) GetPullRequest(_ context.Context, _, _ string, number int) (PullRequestStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("GetPullRequest"); err != nil {
		return PullRequestStatus{}, err
	}
	if st, ok := f.PRStatuses[number]; ok {
		return st, nil
	}
	mergeable := true
	return PullRequestStatus{
		Number: number, State: "open", Mergeable: &mergeable,
		MergeableState: "clean", HeadSHA: "headsha", BaseRef: "main",
	}, nil
}

func (f *Fake) ListFailedChecks(_ context.Context, _, _, ref string) ([]CheckFailure, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ListFailedChecks"); err != nil {
		return nil, err
	}
	return f.FailedChecks[ref], nil
}

// CallsSnapshot returns a copy of the method-call log recorded so far. Safe
// for concurrent use, unlike reading the Calls field directly.
func (f *Fake) CallsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Calls))
	copy(out, f.Calls)
	return out
}

// PRsSnapshot returns a copy of the pull requests created so far. Safe for
// concurrent use, unlike reading the PRs field directly.
func (f *Fake) PRsSnapshot() []PullRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]PullRequest, len(f.PRs))
	copy(out, f.PRs)
	return out
}

// IssueByNumber returns a copy of the issue with the given number, and
// whether it exists. Safe for concurrent use, unlike reading the Issues field
// directly.
func (f *Fake) IssueByNumber(number int) (Issue, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i, ok := f.Issues[number]
	if !ok {
		return Issue{}, false
	}
	i.Labels = append([]string(nil), i.Labels...) // defensive copy of the slice too
	return i, true
}

// SetFailNext sets the error returned by the next call to any method (then
// cleared), mirroring the FailNext field. Safe for concurrent use, unlike
// writing the FailNext field directly.
func (f *Fake) SetFailNext(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FailNext = err
}
