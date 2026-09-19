package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// The pull request body is written by the shem, from the branch's own diff,
// and arrives with the branch-pushed report. These pin the two things that
// decide whether a pull request is useful or even opened at all.
func TestPRBodyFromTheShem(t *testing.T) {
	const desc = "d"
	issue := 2755

	seed := func(t *testing.T, h *api.Handlers) (db.Ticket, string) {
		t.Helper()
		shem := seedShem(t, h, "pr-shem", "prkey")
		ticket := db.Ticket{
			RepoRemote: "https://github.com/org/repo", Branch: "ticket/x",
			Description: desc, Phase: "ready-for-review", AssignedShem: &shem.ID,
			IssueNumber: &issue, BaseBranch: "main", Title: "Import a document from a URL",
			IntakeApproved: true, BodyHash: ghsync.HashDescription(desc),
			ApprovedBodyHash: ghsync.HashDescription(desc),
		}
		if err := h.DB.Create(&ticket).Error; err != nil {
			t.Fatalf("seed ticket: %v", err)
		}
		return ticket, "prkey"
	}

	// setupTicketTest does not register the GitHub routes, and branch-pushed
	// is one of them.
	withGitHubRoutes := func(t *testing.T) (*api.Handlers, *http.ServeMux) {
		t.Helper()
		h, mux := setupTicketTest(t)
		h.RegisterGitHubRoutes(mux)
		return h, mux
	}

	post := func(t *testing.T, mux *http.ServeMux, id, key, body string) int {
		t.Helper()
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/branch-pushed", nil)
		} else {
			payload, _ := json.Marshal(map[string]string{"pr_body": body})
			r = httptest.NewRequest(http.MethodPost, "/api/tickets/"+id+"/branch-pushed",
				strings.NewReader(string(payload)))
			r.Header.Set("Content-Type", "application/json")
		}
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("X-Shem-Name", "pr-shem")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}

	prPayload := func(t *testing.T, h *api.Handlers, id string) ghsync.PRPayload {
		t.Helper()
		var rows []db.GitHubOutbox
		if err := h.DB.Where("ticket_id = ? AND kind = ?", id, ghsync.KindPR).Find(&rows).Error; err != nil {
			t.Fatalf("load outbox: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("pr outbox rows = %d, want exactly 1", len(rows))
		}
		var p ghsync.PRPayload
		if err := json.Unmarshal([]byte(rows[0].Payload), &p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		return p
	}

	t.Run("a generated body reaches the pull request", func(t *testing.T) {
		h, mux := withGitHubRoutes(t)
		ticket, key := seed(t, h)
		body := "## Summary\nDoes the thing.\n\n## Outcome\nIt is done.\nCloses #2755"
		if code := post(t, mux, ticket.ID, key, body); code != http.StatusNoContent {
			t.Fatalf("branch-pushed = %d, want 204", code)
		}
		got := prPayload(t, h, ticket.ID)
		if !strings.Contains(got.Body, "## Summary") {
			t.Errorf("the generated body did not reach the pull request: %q", got.Body)
		}
		if strings.Count(got.Body, "Closes #2755") != 1 {
			t.Errorf("want exactly one closing reference, got %d: %q",
				strings.Count(got.Body, "Closes #2755"), got.Body)
		}
	})

	t.Run("a body that forgot Closes still gets one", func(t *testing.T) {
		// The issue auto-closing on merge is load-bearing, and a role prompt
		// is not a contract — so the orchestrator guarantees the line.
		h, mux := withGitHubRoutes(t)
		ticket, key := seed(t, h)
		if code := post(t, mux, ticket.ID, key, "## Summary\nNo closing line here."); code != http.StatusNoContent {
			t.Fatalf("branch-pushed = %d, want 204", code)
		}
		got := prPayload(t, h, ticket.ID)
		if !strings.Contains(got.Body, "Closes #2755") {
			t.Errorf("the closing reference was not added: %q", got.Body)
		}
		if !strings.Contains(got.Body, "## Summary") {
			t.Errorf("the generated body was discarded: %q", got.Body)
		}
	})

	t.Run("no body still opens the pull request", func(t *testing.T) {
		// A shem that could not generate one, or an older shem that sends
		// none. A missing description must not cost the pull request.
		h, mux := withGitHubRoutes(t)
		ticket, key := seed(t, h)
		if code := post(t, mux, ticket.ID, key, ""); code != http.StatusNoContent {
			t.Fatalf("branch-pushed = %d, want 204", code)
		}
		got := prPayload(t, h, ticket.ID)
		if !strings.Contains(got.Body, "Closes #2755") {
			t.Errorf("fallback body has no closing reference: %q", got.Body)
		}
		if got.Head != ticket.Branch || got.Base != "main" {
			t.Errorf("payload head/base wrong: %+v", got)
		}
	})
}
