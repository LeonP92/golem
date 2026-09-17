package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// A ticket parked in needs-attention must not be resumed automatically.
//
// needs-attention means a run failed and a human has to look. While it was
// resumable, a ticket whose agent stopped for a reason a restart cannot
// change — an unauthenticated nested `claude`, a question only a person can
// answer — re-ran a full implementation pass on every shem restart and
// appended another round of near-identical log entries each time.
func TestResumableExcludesNeedsAttention(t *testing.T) {
	phase := "implement"
	desc := "d"

	tests := []struct {
		name        string
		phase       string
		wantResumed bool
	}{
		{"a mid-flight ticket resumes", "implement", true},
		{"a parked ticket does not", "needs-attention", false},
		{"ready-for-review does not", "ready-for-review", false},
		{"revising does not", "revising", false},
		{"closed does not", "closed", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, mux := setupTicketTest(t)
			shem := seedShem(t, h, "resume-shem", "resumekey")
			n := 11
			ticket := db.Ticket{
				RepoRemote:       "https://github.com/org/repo",
				Branch:           "ticket/x",
				Description:      desc,
				Phase:            tt.phase,
				AssignedShem:     &shem.ID,
				CheckpointPhase:  &phase,
				IssueNumber:      &n,
				IntakeApproved:   true,
				BodyHash:         ghsync.HashDescription(desc),
				ApprovedBodyHash: ghsync.HashDescription(desc),
			}
			if err := h.DB.Create(&ticket).Error; err != nil {
				t.Fatalf("seed ticket: %v", err)
			}

			req := httptest.NewRequest(http.MethodGet, "/api/tickets/resumable", nil)
			req.Header.Set("Authorization", "Bearer resumekey")
			req.Header.Set("X-Shem-Name", "resume-shem")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("GET resumable = %d: %s", w.Code, w.Body.String())
			}
			var claims []api.ClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &claims); err != nil {
				t.Fatalf("decode: %v", err)
			}
			resumed := false
			for _, c := range claims {
				if c.TicketID == ticket.ID {
					resumed = true
				}
			}
			if resumed != tt.wantResumed {
				t.Errorf("phase %q resumed = %v, want %v", tt.phase, resumed, tt.wantResumed)
			}
		})
	}
}
