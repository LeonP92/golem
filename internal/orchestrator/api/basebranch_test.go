package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// Every claim the shem receives must name the base branch.
//
// Resolving a merge conflict means merging a specific branch — `git merge
// origin/main` — and the shem cannot fetch it for the agent without knowing
// which one it is. It cannot read the branch from the worktree's own config
// either: the agent owns that repository and can write it, which is exactly
// why the push was moved to a mirror. The base has to come from the
// orchestrator, with the rest of the claim.
func TestClaimsCarryTheBaseBranch(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "base-shem", "basekey")

	phase := "implement"
	for _, tc := range []struct {
		name, path, phase string
		assigned          bool
		checkpoint        bool
	}{
		{"resumable", "/api/tickets/resumable", "implement", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := db.Ticket{
				RepoRemote: "https://github.com/org/repo", Branch: "ticket/x-abc12345",
				BaseBranch: "release/2026-09", Description: "d", Phase: tc.phase,
			}
			if tc.assigned {
				tk.AssignedShem = &shem.ID
			}
			if tc.checkpoint {
				tk.CheckpointPhase = &phase
			}
			if err := h.DB.Create(&tk).Error; err != nil {
				t.Fatalf("create ticket: %v", err)
			}
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("Authorization", "Bearer basekey")
			req.Header.Set("X-Shem-Name", "base-shem")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s = %d: %s", tc.path, w.Code, w.Body.String())
			}
			var claims []api.ClaimResponse
			if err := json.Unmarshal(w.Body.Bytes(), &claims); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(claims) == 0 {
				t.Fatal("no claims returned")
			}
			if claims[0].BaseBranch != "release/2026-09" {
				t.Errorf("base_branch = %q, want release/2026-09; the shem cannot fetch "+
					"a base branch it was never told about", claims[0].BaseBranch)
			}
		})
	}
}

// The JSON key matters as much as the field: the shem decodes into its own
// struct, so a mismatch here is a silently empty base branch on that side.
func TestClaimResponseBaseBranchJSONKey(t *testing.T) {
	b, err := json.Marshal(api.ClaimResponse{BaseBranch: "main"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["base_branch"] != "main" {
		t.Errorf("base_branch key missing or wrong in %s", b)
	}
}
