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

// TestPhaseReEntryQueuesAFreshLabelWrite covers finding I4: the phase graph
// has cycles, so the intended side effect of a phase write is per (ticket,
// phase TRANSITION), not per (ticket, phase). Before the fix the second
// arrival at a phase reused the first arrival's idempotency key, the insert
// was suppressed, and the issue kept advertising a phase the ticket had
// already left — permanently, on a repo quiet enough that the poll keeps
// answering 304 and reconcile never runs.
//
// Verified against HEAD ff2c226 before the fix: re-entry produced 0 extra
// label rows.
func TestPhaseReEntryQueuesAFreshLabelWrite(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "cycle-shem", "cyclekey")

	n := 91
	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Title:        "t",
		Branch:       "ticket/cycle",
		Description:  "d",
		Phase:        "implement",
		AssignedShem: &shem.ID,
		IssueNumber:  &n,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	patch := func(phase string) int {
		body, _ := json.Marshal(map[string]string{"phase": phase})
		req := httptest.NewRequest(http.MethodPatch, "/api/tickets/"+ticket.ID+"/phase", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer cyclekey")
		req.Header.Set("X-Shem-Name", "cycle-shem")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}

	// A revise cycle: the reviewer requests changes, the shem re-implements,
	// and the ticket arrives at ready-for-review a second time.
	steps := []string{"ready-for-review", "implement", "ready-for-review"}
	for i, phase := range steps {
		if code := patch(phase); code != http.StatusNoContent {
			t.Fatalf("step %d (-> %s): status = %d, want 204", i, phase, code)
		}
	}

	var rows []db.GitHubOutbox
	h.DB.Where("ticket_id = ? AND kind = ?", ticket.ID, ghsync.KindLabel).
		Order("id asc").Find(&rows)
	if len(rows) != len(steps) {
		keys := make([]string, 0, len(rows))
		for _, r := range rows {
			keys = append(keys, r.IdempotencyKey)
		}
		t.Fatalf("label rows = %d (%v), want %d — one per phase transition", len(rows), keys, len(steps))
	}
	for i, r := range rows {
		if got, want := payloadPhase(t, r.Payload), steps[i]; got != want {
			t.Errorf("row %d phase = %q, want %q", i, got, want)
		}
	}

	// The milestone comment is deliberately NOT per-transition: re-entering
	// implement must not repost "Plan approved; implementation starting."
	var comments int64
	h.DB.Model(&db.GitHubOutbox{}).
		Where("ticket_id = ? AND kind = ?", ticket.ID, ghsync.KindComment).Count(&comments)
	if comments != 2 {
		t.Errorf("comment rows = %d, want 2 (one per distinct milestone)", comments)
	}
}

// TestBarePhaseActionsQueueTheirLabel covers the second route to I4's drift:
// requeue and needs-attention changed a linked ticket's phase with a bare
// Update and never queued the matching label, so the issue kept whatever
// golem:* label it had until a reconcile happened to run.
//
// Verified against HEAD ff2c226 before the fix: 0 label rows for both.
func TestBarePhaseActionsQueueTheirLabel(t *testing.T) {
	cases := []struct {
		name      string
		action    string
		fromPhase string
		wantPhase string
	}{
		{name: "requeue", action: "requeue", fromPhase: "implement", wantPhase: "unassigned"},
		{name: "needs-attention", action: "needs-attention", fromPhase: "implement", wantPhase: "needs-attention"},
		{name: "requeue on an unlinked ticket queues nothing", action: "requeue", fromPhase: "implement", wantPhase: ""},
		{name: "needs-attention on an unlinked ticket queues nothing", action: "needs-attention", fromPhase: "implement", wantPhase: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)
			linked := tc.wantPhase != ""

			ticket := db.Ticket{
				RepoRemote: "https://github.com/org/repo", Title: "t",
				Branch: "b", Description: "d", Phase: tc.fromPhase,
			}
			if linked {
				n := 92
				ticket.IssueNumber = &n
			}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			body, _ := json.Marshal(map[string]string{"action": tc.action})
			req := httptest.NewRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/actions", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			withSession(req, cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
			}

			var rows []db.GitHubOutbox
			h.DB.Where("ticket_id = ? AND kind = ?", ticket.ID, ghsync.KindLabel).Find(&rows)
			if !linked {
				if len(rows) != 0 {
					t.Fatalf("label rows = %d, want 0 for an unlinked ticket", len(rows))
				}
				return
			}
			if len(rows) != 1 {
				t.Fatalf("label rows = %d, want 1", len(rows))
			}
			if got := payloadPhase(t, rows[0].Payload); got != tc.wantPhase {
				t.Errorf("queued label phase = %q, want %q", got, tc.wantPhase)
			}
		})
	}
}

// payloadPhase decodes a KindLabel payload and returns its phase.
func payloadPhase(t *testing.T, payload string) string {
	t.Helper()
	var p ghsync.LabelPayload
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("decode label payload %q: %v", payload, err)
	}
	return p.Phase
}
