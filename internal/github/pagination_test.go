package github_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
)

// TestListIssuesSincePagination covers the pagination loop, which had no test
// of any kind: pageNum, the first-page ETag capture and the IsPullRequest
// skip were all unexercised, so mutation M27 (keep the LAST page's ETag
// instead of the first's) survived the whole suite.
//
// It also pins minor m7: If-None-Match must go out on the first page only.
// The stored ETag identifies the listing as a whole; sending it on page 2
// risks a 304 that would discard page 1's already-collected issues and
// report "nothing changed", and because IngestRepo's 304 path moves neither
// the cursor nor the ETag, the next poll repeats it identically.
func TestListIssuesSincePagination(t *testing.T) {
	var conditional []string // the If-None-Match value seen on each request
	var pages []string       // the page= parameter seen on each request

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conditional = append(conditional, r.Header.Get("If-None-Match"))
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		w.Header().Set("Content-Type", "application/json")
		if page == "" {
			w.Header().Set("ETag", `W/"first-page"`)
			w.Header().Set("Link", fmt.Sprintf(`<%s?page=2>; rel="next"`, r.URL.Path))
			// One real issue and one pull request, which the issues
			// endpoint also returns and which must be skipped.
			_, _ = w.Write([]byte(`[
				{"number":1,"title":"one","state":"open","updated_at":"2026-01-01T00:00:00Z"},
				{"number":2,"title":"a pr","state":"open","pull_request":{"url":"x"}}
			]`))
			return
		}
		w.Header().Set("ETag", `W/"second-page"`)
		_, _ = w.Write([]byte(`[{"number":3,"title":"three","state":"open","updated_at":"2026-01-02T00:00:00Z"}]`))
	}))
	defer srv.Close()

	c, err := github.New("token", srv.URL+"/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	page, err := c.ListIssuesSince(context.Background(), "org", "repo", "golem",
		time.Unix(0, 0), `W/"stored"`)
	if err != nil {
		t.Fatalf("ListIssuesSince: %v", err)
	}

	if len(pages) != 2 || pages[0] != "" || pages[1] != "2" {
		t.Fatalf("requested pages = %v, want the first page then page 2", pages)
	}
	if conditional[0] != `W/"stored"` {
		t.Errorf("first page If-None-Match = %q, want the stored ETag", conditional[0])
	}
	if conditional[1] != "" {
		t.Errorf("page 2 If-None-Match = %q, want it absent — a 304 there would silently discard page 1",
			conditional[1])
	}

	if page.ETag != `W/"first-page"` {
		t.Errorf("ETag = %q, want the FIRST page's — that is what identifies the listing", page.ETag)
	}
	if len(page.Issues) != 2 {
		t.Fatalf("issues = %d, want 2 (both pages, pull request skipped): %+v", len(page.Issues), page.Issues)
	}
	if page.Issues[0].Number != 1 || page.Issues[1].Number != 3 {
		t.Errorf("issue numbers = %d, %d; want 1, 3", page.Issues[0].Number, page.Issues[1].Number)
	}
	if page.NotModified {
		t.Error("NotModified set on a 200 response")
	}
}

// TestListIssuesSinceOmitsAZeroCursor pins the parameter that stopped the
// integration working against real GitHub on its very first request.
//
// The cursor starts as a zero time.Time, which RFC3339-formats to
// 0001-01-01T00:00:00Z. GitHub answers that with
//
//	422 The since parameter needs to be in ISO 8601 format: YYYY-MM-DDTHH:MM:SSZ
//
// and the failure is not self-healing: the cursor only advances after a
// successful poll, so a repository that has never synced can never sync. No
// test caught it because github.Fake records the argument rather than
// validating it, and every test in the tree runs against the fake — the kind
// of gap that only a real server finds.
func TestListIssuesSinceOmitsAZeroCursor(t *testing.T) {
	tests := []struct {
		name     string
		since    time.Time
		wantSent bool
	}{
		{"first poll, no cursor yet", time.Time{}, false},
		{"a real cursor is still sent", time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw string
			var present bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw = r.URL.Query().Get("since")
				_, present = r.URL.Query()["since"]
				// Reject exactly as GitHub does, so this test fails on the
				// symptom the operator saw and not merely on a string compare.
				if present && strings.HasPrefix(raw, "0001-01-01") {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"message":"The since parameter needs to be in ISO 8601 format: YYYY-MM-DDTHH:MM:SSZ"}`))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[]`))
			}))
			defer srv.Close()

			c, err := github.New("token", srv.URL+"/")
			if err != nil {
				t.Fatalf("github.New: %v", err)
			}
			if _, err = c.ListIssuesSince(context.Background(), "o", "r", "golem", tt.since, ""); err != nil {
				t.Fatalf("ListIssuesSince: %v", err)
			}
			if present != tt.wantSent {
				t.Errorf("since present = %v, want %v (raw %q)", present, tt.wantSent, raw)
			}
			if tt.wantSent && raw != "2026-03-04T05:06:07Z" {
				t.Errorf("since = %q, want the cursor in RFC3339", raw)
			}
		})
	}
}
