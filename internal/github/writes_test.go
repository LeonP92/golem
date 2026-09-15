package github_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/github"
)

func TestWritePath(t *testing.T) {
	var gotMethod, gotPath, gotBody string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/org/repo/pulls":
			_, _ = w.Write([]byte(`{"number":42,"html_url":"https://github.com/org/repo/pull/42"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/issues/7/labels":
			// AddLabelsToIssue decodes the response body as a label array, not
			// an issue object.
			_, _ = w.Write([]byte(`[{"name":"golem:plan"}]`))
		default:
			_, _ = w.Write([]byte(`{"number":7,"title":"t","state":"closed"}`))
		}
	}))
	defer srv.Close()

	c, err := github.New("token", srv.URL+"/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	t.Run("CreateComment", func(t *testing.T) {
		if err := c.CreateComment(ctx, "org", "repo", 7, "hello"); err != nil {
			t.Fatalf("CreateComment: %v", err)
		}
		if gotMethod != http.MethodPost || gotPath != "/repos/org/repo/issues/7/comments" {
			t.Errorf("got %s %s, want POST /repos/org/repo/issues/7/comments", gotMethod, gotPath)
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if payload["body"] != "hello" {
			t.Errorf("body = %q, want hello", payload["body"])
		}
	})

	t.Run("AddLabel", func(t *testing.T) {
		if err := c.AddLabel(ctx, "org", "repo", 7, "golem:plan"); err != nil {
			t.Fatalf("AddLabel: %v", err)
		}
		if gotPath != "/repos/org/repo/issues/7/labels" {
			t.Errorf("path = %q, want /repos/org/repo/issues/7/labels", gotPath)
		}
	})

	t.Run("RemoveLabel", func(t *testing.T) {
		if err := c.RemoveLabel(ctx, "org", "repo", 7, "golem:plan"); err != nil {
			t.Fatalf("RemoveLabel: %v", err)
		}
		if gotMethod != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", gotMethod)
		}
	})

	t.Run("SetIssueState", func(t *testing.T) {
		if err := c.SetIssueState(ctx, "org", "repo", 7, "closed"); err != nil {
			t.Fatalf("SetIssueState: %v", err)
		}
		if gotMethod != http.MethodPatch {
			t.Errorf("method = %q, want PATCH", gotMethod)
		}
	})

	t.Run("CreatePullRequest", func(t *testing.T) {
		pr, err := c.CreatePullRequest(ctx, "org", "repo", "ticket/x", "main", "title", "Closes #7", true)
		if err != nil {
			t.Fatalf("CreatePullRequest: %v", err)
		}
		if pr.Number != 42 || pr.HTMLURL == "" {
			t.Errorf("pr = %+v, want number 42 with URL", pr)
		}
	})

	t.Run("CreateIssue", func(t *testing.T) {
		issue, err := c.CreateIssue(ctx, "org", "repo", "title", "body", []string{"golem"})
		if err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if gotMethod != http.MethodPost || gotPath != "/repos/org/repo/issues" {
			t.Errorf("got %s %s, want POST /repos/org/repo/issues", gotMethod, gotPath)
		}
		if issue.Number != 7 {
			t.Errorf("issue = %+v, want number 7", issue)
		}
	})
}

// TestWritePathErrors verifies each write method wraps and propagates a
// non-2xx response as an error, and that RemoveLabel treats a 404 as success
// rather than an error.
func TestWritePathErrors(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
		call    func(c github.Client) error
	}{
		{
			name:    "CreateComment propagates server error",
			status:  http.StatusInternalServerError,
			wantErr: true,
			call: func(c github.Client) error {
				return c.CreateComment(context.Background(), "org", "repo", 7, "hi")
			},
		},
		{
			name:    "AddLabel propagates server error",
			status:  http.StatusInternalServerError,
			wantErr: true,
			call: func(c github.Client) error {
				return c.AddLabel(context.Background(), "org", "repo", 7, "golem")
			},
		},
		{
			name:    "RemoveLabel propagates server error",
			status:  http.StatusInternalServerError,
			wantErr: true,
			call: func(c github.Client) error {
				return c.RemoveLabel(context.Background(), "org", "repo", 7, "golem")
			},
		},
		{
			name:    "RemoveLabel treats not-found as success",
			status:  http.StatusNotFound,
			wantErr: false,
			call: func(c github.Client) error {
				return c.RemoveLabel(context.Background(), "org", "repo", 7, "golem")
			},
		},
		{
			name:    "SetIssueState propagates server error",
			status:  http.StatusInternalServerError,
			wantErr: true,
			call: func(c github.Client) error {
				return c.SetIssueState(context.Background(), "org", "repo", 7, "closed")
			},
		},
		{
			name:    "CreatePullRequest propagates server error",
			status:  http.StatusInternalServerError,
			wantErr: true,
			call: func(c github.Client) error {
				_, err := c.CreatePullRequest(context.Background(), "org", "repo", "head", "base", "t", "b", false)
				return err
			},
		},
		{
			name:    "CreateIssue propagates server error",
			status:  http.StatusInternalServerError,
			wantErr: true,
			call: func(c github.Client) error {
				_, err := c.CreateIssue(context.Background(), "org", "repo", "t", "b", nil)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = tt.call(c)
			if tt.wantErr && err == nil {
				t.Fatal("want error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("want no error, got %v", err)
			}
		})
	}
}
