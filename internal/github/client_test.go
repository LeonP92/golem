package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/github"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		token   string
		apiBase string
		wantErr bool
	}{
		{
			name:    "defaults to github.com when apiBase is empty",
			token:   "token",
			apiBase: "",
		},
		{
			name:    "accepts an explicit enterprise base",
			token:   "token",
			apiBase: "https://ghe.example.com/api/v3/",
		},
		{
			name:    "rejects an unparsable apiBase",
			token:   "token",
			apiBase: "http://%zz",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := github.New(tt.token, tt.apiBase)
			if tt.wantErr {
				if err == nil {
					t.Fatal("New: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if c == nil {
				t.Fatal("New: want non-nil client")
			}
		})
	}
}

func TestGetIssue(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantNumber int
		wantTitle  string
	}{
		{
			name:       "returns the issue",
			status:     http.StatusOK,
			body:       `{"number":42,"title":"Fix the thing","state":"open","labels":[{"name":"bug"}]}`,
			wantNumber: 42,
			wantTitle:  "Fix the thing",
		},
		{
			name:    "propagates a not-found error",
			status:  http.StatusNotFound,
			body:    `{"message":"Not Found"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/repos/org/repo/issues/42") {
					t.Errorf("path = %q, want suffix /repos/org/repo/issues/42", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			issue, err := c.GetIssue(context.Background(), "org", "repo", 42)
			if tt.wantErr {
				if err == nil {
					t.Fatal("GetIssue: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetIssue: %v", err)
			}
			if issue.Number != tt.wantNumber || issue.Title != tt.wantTitle {
				t.Errorf("issue = %+v, want number %d / title %q", issue, tt.wantNumber, tt.wantTitle)
			}
			if !issue.HasLabel("bug") {
				t.Error("HasLabel(bug) = false, want true")
			}
			if issue.HasLabel("nonexistent") {
				t.Error("HasLabel(nonexistent) = true, want false")
			}
		})
	}
}

func TestDefaultBranch(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
		want    string
	}{
		{
			name:   "returns the default branch",
			status: http.StatusOK,
			body:   `{"default_branch":"main"}`,
			want:   "main",
		},
		{
			name:    "propagates a server error",
			status:  http.StatusInternalServerError,
			body:    `{"message":"boom"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/repos/org/repo") {
					t.Errorf("path = %q, want suffix /repos/org/repo", r.URL.Path)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := c.DefaultBranch(context.Background(), "org", "repo")
			if tt.wantErr {
				if err == nil {
					t.Fatal("DefaultBranch: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("DefaultBranch: %v", err)
			}
			if got != tt.want {
				t.Errorf("DefaultBranch = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUnimplementedStubs locks in the "not implemented" behavior of the
// write-path methods Task 2 replaces, so a future implementation must
// deliberately remove these stubs rather than leave one behind by accident.
func TestUnimplementedStubs(t *testing.T) {
	c, err := github.New("token", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, err := c.CreateIssue(ctx, "org", "repo", "title", "body", nil); err == nil {
		t.Error("CreateIssue: want error, got nil")
	}
	if err := c.SetIssueState(ctx, "org", "repo", 1, "closed"); err == nil {
		t.Error("SetIssueState: want error, got nil")
	}
	if err := c.AddLabel(ctx, "org", "repo", 1, "golem"); err == nil {
		t.Error("AddLabel: want error, got nil")
	}
	if err := c.RemoveLabel(ctx, "org", "repo", 1, "golem"); err == nil {
		t.Error("RemoveLabel: want error, got nil")
	}
	if err := c.CreateComment(ctx, "org", "repo", 1, "body"); err == nil {
		t.Error("CreateComment: want error, got nil")
	}
	if _, err := c.CreatePullRequest(ctx, "org", "repo", "head", "base", "title", "body", false); err == nil {
		t.Error("CreatePullRequest: want error, got nil")
	}
}
