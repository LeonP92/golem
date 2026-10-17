package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// Self-verification is the default for every ticket: an ordinary ticket
// claims with verify_disabled unset, and a ticket that opted out at
// creation keeps its opt-out in the claim — including an epic child,
// whose run never sets the flag.
func TestClaimCarriesVerifyOptOut(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "shem-a", "testkey")
	optedOut := db.Ticket{ID: "no-verify", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b1", Title: "t", Description: "d", Phase: "unassigned",
		VerifyDisabled: true}
	plain := db.Ticket{ID: "verifies", RepoRemote: "https://github.com/org/repo",
		BaseBranch: "main", Branch: "b2", Title: "t", Description: "d", Phase: "unassigned"}
	h.DB.Create(&optedOut)
	h.DB.Create(&plain)

	claim := func(id string) api.ClaimResponse {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+id+"/claim", "shem-a", "testkey"))
		if w.Code != http.StatusOK {
			t.Fatalf("claim %s = %d: %s", id, w.Code, w.Body.String())
		}
		var resp api.ClaimResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}
	if resp := claim("no-verify"); !resp.VerifyDisabled {
		t.Fatal("ticket's verify opt-out lost at claim")
	}
	if resp := claim("verifies"); resp.VerifyDisabled {
		t.Fatal("ordinary ticket opted itself out of self-verification")
	}
}
