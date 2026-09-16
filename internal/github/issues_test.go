package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
)

func TestListIssuesSince(t *testing.T) {
	tests := []struct {
		name       string
		reqETag    string // etag passed into ListIssuesSince, simulating a stored value
		status     int
		body       string
		respETag   string
		wantCount  int
		wantNotMod bool
	}{
		{
			name:   "returns labeled issues",
			status: http.StatusOK,
			body: `[{"number":7,"title":"Add rate limiting","body":"details",
				"state":"open","html_url":"https://github.com/org/repo/issues/7",
				"updated_at":"2026-09-15T10:00:00Z","labels":[{"name":"golem"}]}]`,
			respETag:  `W/"abc"`,
			wantCount: 1,
		},
		{
			name:       "304 reports not modified",
			reqETag:    `W/"prev"`,
			status:     http.StatusNotModified,
			body:       "",
			respETag:   `W/"prev"`,
			wantNotMod: true,
		},
		{
			name:      "empty list",
			status:    http.StatusOK,
			body:      `[]`,
			wantCount: 0,
		},
		{
			name:      "sends the stored etag as If-None-Match",
			reqETag:   `W/"cached"`,
			status:    http.StatusOK,
			body:      `[]`,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("labels"); got != "golem" {
					t.Errorf("labels query = %q, want golem", got)
				}
				if got := r.URL.Query().Get("state"); got != "all" {
					t.Errorf("state query = %q, want all", got)
				}
				if got := r.Header.Get("If-None-Match"); got != tt.reqETag {
					t.Errorf("If-None-Match = %q, want %q", got, tt.reqETag)
				}
				if tt.respETag != "" {
					w.Header().Set("ETag", tt.respETag)
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			page, err := c.ListIssuesSince(context.Background(), "org", "repo", "golem",
				time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), tt.reqETag)
			if err != nil {
				t.Fatalf("ListIssuesSince: %v", err)
			}
			if page.NotModified != tt.wantNotMod {
				t.Errorf("NotModified = %v, want %v", page.NotModified, tt.wantNotMod)
			}
			if page.ETag != tt.respETag {
				t.Errorf("ETag = %q, want %q", page.ETag, tt.respETag)
			}
			if len(page.Issues) != tt.wantCount {
				t.Fatalf("got %d issues, want %d", len(page.Issues), tt.wantCount)
			}
			if tt.wantCount > 0 {
				got := page.Issues[0]
				if got.Number != 7 || got.Title != "Add rate limiting" || got.State != "open" {
					t.Errorf("issue = %+v, want number 7 / title / open", got)
				}
				if !got.HasLabel("golem") {
					t.Error("HasLabel(golem) = false, want true")
				}
			}
		})
	}
}
