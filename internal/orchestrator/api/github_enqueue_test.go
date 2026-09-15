package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// TestPhaseChangeEnqueuesGitHubWrites asserts that a shem advancing a
// ticket's phase queues label and milestone-comment outbox rows only when
// the ticket is linked to a GitHub issue, and that repeating the same phase
// transition never grows the outbox further.
func TestPhaseChangeEnqueuesGitHubWrites(t *testing.T) {
	cases := []struct {
		name        string
		linked      bool
		wantLabel   int
		wantComment int
	}{
		{name: "linked ticket queues label and milestone comment", linked: true, wantLabel: 1, wantComment: 1},
		{name: "unlinked ticket queues nothing", linked: false, wantLabel: 0, wantComment: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mux := setupTicketTest(t)
			shem := seedShem(t, h, "phase-enqueue-shem", "phasekey")

			ticket := db.Ticket{
				RepoRemote:   "https://github.com/org/repo",
				Title:        "t",
				Branch:       "ticket/phase-enqueue",
				Description:  "d",
				Phase:        "brainstorm",
				AssignedShem: &shem.ID,
			}
			if tc.linked {
				n := 7
				ticket.IssueNumber = &n
			}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			doPhaseUpdate := func() int {
				body, _ := json.Marshal(map[string]string{"phase": "implement"})
				req := httptest.NewRequest(http.MethodPatch, "/api/tickets/"+ticket.ID+"/phase", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer phasekey")
				req.Header.Set("X-Shem-Name", "phase-enqueue-shem")
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				return w.Code
			}

			if code := doPhaseUpdate(); code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", code)
			}

			var rows []db.GitHubOutbox
			h.DB.Where("ticket_id = ?", ticket.ID).Find(&rows)
			kinds := map[string]int{}
			for _, row := range rows {
				kinds[row.Kind]++
			}
			if kinds[ghsync.KindLabel] != tc.wantLabel {
				t.Errorf("label rows = %d, want %d", kinds[ghsync.KindLabel], tc.wantLabel)
			}
			if kinds[ghsync.KindComment] != tc.wantComment {
				t.Errorf("comment rows = %d, want %d", kinds[ghsync.KindComment], tc.wantComment)
			}

			// Re-sending the same phase must not queue a second pair (or,
			// for the unlinked case, must continue to queue nothing).
			if code := doPhaseUpdate(); code != http.StatusNoContent {
				t.Fatalf("second status = %d, want 204", code)
			}
			var after int64
			h.DB.Model(&db.GitHubOutbox{}).Where("ticket_id = ?", ticket.ID).Count(&after)
			if after != int64(len(rows)) {
				t.Errorf("outbox rows grew from %d to %d on a repeated phase", len(rows), after)
			}
		})
	}
}

// TestActionCloseEnqueuesGitHubCloseWrite asserts that a human closing a
// ticket queues a close_issue outbox row only when the ticket is linked to a
// GitHub issue, and that a second close attempt is rejected as a conflict
// without growing the outbox.
func TestActionCloseEnqueuesGitHubCloseWrite(t *testing.T) {
	cases := []struct {
		name      string
		linked    bool
		wantClose int
	}{
		{name: "linked ticket queues close_issue", linked: true, wantClose: 1},
		{name: "unlinked ticket queues nothing", linked: false, wantClose: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)

			ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "ready-for-review"}
			if tc.linked {
				n := 42
				ticket.IssueNumber = &n
			}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			body, _ := json.Marshal(map[string]string{"action": "close"})
			doClose := func() int {
				req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions", bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.AddCookie(cookie)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				return w.Code
			}

			if code := doClose(); code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204", code)
			}

			var rows []db.GitHubOutbox
			h.DB.Where("ticket_id = ?", ticket.ID).Find(&rows)
			kinds := map[string]int{}
			for _, row := range rows {
				kinds[row.Kind]++
			}
			if kinds[ghsync.KindClose] != tc.wantClose {
				t.Errorf("close rows = %d, want %d", kinds[ghsync.KindClose], tc.wantClose)
			}

			var got db.Ticket
			h.DB.First(&got, "id = ?", ticket.ID)
			if got.Phase != "closed" {
				t.Errorf("expected phase=closed, got %q", got.Phase)
			}

			// A ticket that is already closed must be rejected as a
			// conflict, and must not grow the outbox further.
			if code := doClose(); code != http.StatusConflict {
				t.Errorf("second close status = %d, want 409", code)
			}
			var after int64
			h.DB.Model(&db.GitHubOutbox{}).Where("ticket_id = ?", ticket.ID).Count(&after)
			if after != int64(len(rows)) {
				t.Errorf("outbox rows grew from %d to %d after repeated close", len(rows), after)
			}
		})
	}
}
