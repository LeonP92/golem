package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// A shem API key must not be able to resolve an approval.
//
// Review: "A shem API key can resolve any kind of human input, including
// approval. Once the agent has the key (see the /proc issue), it can approve
// its own spec or plan, which skips the human gate. Add AND kind =
// 'feedback'; that's the only kind AckInput needs."
//
// The shem's only legitimate use of this endpoint is consumeFeedback, which
// acks the feedback it has just read. Approvals are resolved by a person in
// the UI, through the session-authenticated human-action endpoints, and
// nothing on the shem side has any business closing one.
//
// This still matters after the brainstorm and plan gates became automatic.
// Those stages no longer create approval rows, but intake and any future
// human gate do, and a credential that can resolve "any unresolved input on a
// ticket I own" is one prompt injection away from resolving the wrong one.
func TestResolveHumanInput_ShemCannotResolveAnApproval(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "acking-shem", "ackkey")

	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Branch: "b",
		Description: "d", Phase: "brainstorm", AssignedShem: &shem.ID,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	for _, tc := range []struct {
		kind       string
		wantStatus int
		why        string
	}{
		{"feedback", http.StatusNoContent, "the shem must still be able to ack feedback it has consumed"},
		{"approval", http.StatusNotFound, "a shem key must not resolve an approval — that is the human gate"},
		{"question_answer", http.StatusNotFound, "only feedback is the shem's to resolve"},
		{"blocker_ack", http.StatusNotFound, "only feedback is the shem's to resolve"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			input := db.HumanInput{TicketID: ticket.ID, Kind: tc.kind, Prompt: "p"}
			if err := h.DB.Create(&input).Error; err != nil {
				t.Fatalf("create input: %v", err)
			}
			body, _ := json.Marshal(map[string]string{"response": "approved by me"})
			req := httptest.NewRequest(http.MethodPatch,
				fmt.Sprintf("/api/tickets/%s/human-inputs/%d", ticket.ID, input.ID),
				bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer ackkey")
			req.Header.Set("X-Shem-Name", "acking-shem")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d: %s", w.Code, tc.wantStatus, tc.why)
			}

			var after db.HumanInput
			if err := h.DB.First(&after, input.ID).Error; err != nil {
				t.Fatalf("reload input: %v", err)
			}
			resolved := after.ResolvedAt != nil
			if want := tc.kind == "feedback"; resolved != want {
				t.Errorf("resolved = %v, want %v: %s", resolved, want, tc.why)
			}
		})
	}
}
