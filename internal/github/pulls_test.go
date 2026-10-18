package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/github"
)

// TestCreatePullRequestClassifiesUnprocessable pins the 422 detection that
// makes a redelivered KindPR row converge instead of parking (finding I2).
// The classification is by status code, not by message text, so the table
// covers a 422 that does not say "already exists" and a non-422 failure that
// does.
func TestCreatePullRequestClassifiesUnprocessable(t *testing.T) {
	cases := []struct {
		name             string
		status           int
		body             string
		wantUnprocessble bool
	}{
		{
			name:             "422 already exists",
			status:           http.StatusUnprocessableEntity,
			body:             `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for org:ticket/x."}]}`,
			wantUnprocessble: true,
		},
		{
			name:             "422 for some other reason",
			status:           http.StatusUnprocessableEntity,
			body:             `{"message":"Validation Failed","errors":[{"resource":"PullRequest","field":"base","code":"invalid"}]}`,
			wantUnprocessble: true,
		},
		{
			name:             "500 mentioning already exists",
			status:           http.StatusInternalServerError,
			body:             `{"message":"A pull request already exists"}`,
			wantUnprocessble: false,
		},
		{
			name:             "403 rate limited",
			status:           http.StatusForbidden,
			body:             `{"message":"API rate limit exceeded"}`,
			wantUnprocessble: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.CreatePullRequest(context.Background(), "org", "repo",
				"ticket/x", "main", "t", "b", true)
			if err == nil {
				t.Fatal("CreatePullRequest returned no error")
			}
			if got := errors.Is(err, github.ErrPullRequestUnprocessable); got != tc.wantUnprocessble {
				t.Errorf("errors.Is(err, ErrPullRequestUnprocessable) = %v, want %v (err = %v)",
					got, tc.wantUnprocessble, err)
			}
		})
	}
}

// TestFindPullRequest covers the lookup a 422 falls back to: the head filter
// is qualified with the owner the way GitHub requires, and an empty list is
// reported as "not found" rather than as an error.
func TestFindPullRequest(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantFound bool
		wantNum   int
	}{
		{
			name:      "existing pull request",
			body:      `[{"number":42,"html_url":"https://github.com/org/repo/pull/42"}]`,
			wantFound: true, wantNum: 42,
		},
		{name: "no pull request for this head", body: `[]`, wantFound: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("head")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			pr, found, err := c.FindPullRequest(context.Background(), "org", "repo", "ticket/x")
			if err != nil {
				t.Fatalf("FindPullRequest: %v", err)
			}
			if gotPath != "/repos/org/repo/pulls" {
				t.Errorf("path = %q, want /repos/org/repo/pulls", gotPath)
			}
			if gotQuery != "org:ticket/x" {
				t.Errorf("head filter = %q, want org:ticket/x", gotQuery)
			}
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if found && pr.Number != tc.wantNum {
				t.Errorf("pr.Number = %d, want %d", pr.Number, tc.wantNum)
			}
		})
	}
}

// TestMergePullRequest covers the pinned-head merge: the SHA pin goes in the
// request body, a 409 maps to ErrMergeConflictHead (stale validation — the
// caller must re-read), 403 maps to ErrMergeForbidden, a 405 (not mergeable
// right now, e.g. checks pending) stays retryable, and a successful merge
// returns the merge commit SHA.
func TestMergePullRequest(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantErr   error
		retryable bool
		wantSHA   string
	}{
		{name: "merged", status: http.StatusOK,
			body: `{"merged":true,"sha":"deadbeef","message":"ok"}`, wantSHA: "deadbeef"},
		{name: "head moved", status: http.StatusConflict,
			body: `{"message":"Head branch was modified"}`, wantErr: github.ErrMergeConflictHead},
		{name: "protected branch", status: http.StatusMethodNotAllowed,
			body: `{"message":"Required status check is expected"}`, retryable: true},
		{name: "forbidden", status: http.StatusForbidden,
			body: `{"message":"Resource not accessible"}`, wantErr: github.ErrMergeForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			sha, err := c.MergePullRequest(context.Background(), "org", "repo", 42, "headsha")
			if tc.retryable {
				if err == nil || errors.Is(err, github.ErrMergeForbidden) || errors.Is(err, github.ErrMergeConflictHead) {
					t.Fatalf("405 must be a plain retryable error, got %v", err)
				}
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("errors.Is(err, %v) = false (err = %v)", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("MergePullRequest: %v", err)
			}
			if sha != tc.wantSHA {
				t.Errorf("sha = %q, want %q", sha, tc.wantSHA)
			}
			if gotBody["sha"] != "headsha" || gotBody["merge_method"] != "merge" {
				t.Errorf("request body = %v, want sha pin and merge method", gotBody)
			}
		})
	}
}

// TestFakeMergePullRequest pins the fake to the same contract so lane tests
// can exercise the stale-head path without a server.
func TestFakeMergePullRequest(t *testing.T) {
	f := github.NewFake()
	f.PRStatuses = map[int]github.PullRequestStatus{7: github.PullRequestStatus{Number: 7, State: "open", HeadSHA: "oldsha"}}
	f.PRStatuses[7] = github.PullRequestStatus{Number: 7, State: "open", HeadSHA: "oldsha"}
	if _, err := f.MergePullRequest(context.Background(), "o", "r", 7, "newsha"); !errors.Is(err, github.ErrMergeConflictHead) {
		t.Fatalf("stale head must fail closed: %v", err)
	}
	sha, err := f.MergePullRequest(context.Background(), "o", "r", 7, "oldsha")
	if err != nil || sha == "" {
		t.Fatalf("matching head merges: sha=%q err=%v", sha, err)
	}
}
