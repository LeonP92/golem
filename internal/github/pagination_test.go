package github_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
