package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
)

// seedReapedTicket creates a ticket the heartbeat reaper released from
// shem while it was in phase.
func seedReapedTicket(t *testing.T, h *api.Handlers, shem db.Shem, phase string) db.Ticket {
	t.Helper()
	ticket := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Branch: "ticket/reaped",
		Description: "reaped", Phase: "unassigned",
		ReapedFromShem: &shem.ID, ReapedFromPhase: phase,
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}
	return ticket
}

func shemRequest(method, path, name, key string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Shem-Name", name)
	return req
}

func TestReapedTickets_ListsOnlyTheCallersReleasedTickets(t *testing.T) {
	h, mux := setupTicketTest(t)
	a := seedShem(t, h, "shem-a", "key-a")
	b := seedShem(t, h, "shem-b", "key-b")
	mine := seedReapedTicket(t, h, a, "implement")
	seedReapedTicket(t, h, b, "plan")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodGet, "/api/tickets/reaped", "shem-a", "key-a"))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got []struct {
		TicketID string `json:"ticket_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].TicketID != mine.ID {
		t.Errorf("got %+v, want only %s", got, mine.ID)
	}
}

// The shem that was still running a reaped ticket takes it back in the phase
// it was in, and the reaped record is cleared so it cannot happen twice.
func TestReclaim_RestoresOwnershipAndPhase(t *testing.T) {
	h, mux := setupTicketTest(t)
	a := seedShem(t, h, "shem-a", "key-a")
	ticket := seedReapedTicket(t, h, a, "implement")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/reclaim", "shem-a", "key-a"))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "implement" {
		t.Errorf("phase = %q, want implement", got.Phase)
	}
	if got.AssignedShem == nil || *got.AssignedShem != a.ID {
		t.Errorf("assigned_shem = %v, want %d", got.AssignedShem, a.ID)
	}
	if got.ReapedFromShem != nil || got.ReapedFromPhase != "" {
		t.Errorf("reaped record not cleared: %v %q", got.ReapedFromShem, got.ReapedFromPhase)
	}
}

func TestReclaim_RefusesAnotherShem(t *testing.T) {
	h, mux := setupTicketTest(t)
	a := seedShem(t, h, "shem-a", "key-a")
	seedShem(t, h, "shem-b", "key-b")
	ticket := seedReapedTicket(t, h, a, "implement")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/reclaim", "shem-b", "key-b"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", w.Code)
	}
}

// Once anyone claims the ticket normally, the reaped record is gone: the
// original shem must not be able to take it back from under the new owner,
// or after the new owner is requeued by a human.
func TestClaim_ClearsTheReapedRecord(t *testing.T) {
	h, mux := setupTicketTest(t)
	a := seedShem(t, h, "shem-a", "key-a")
	seedShem(t, h, "shem-b", "key-b")
	ticket := seedReapedTicket(t, h, a, "implement")

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/claim", "shem-b", "key-b"))
	if w.Code != http.StatusOK {
		t.Fatalf("claim status %d: %s", w.Code, w.Body.String())
	}
	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.ReapedFromShem != nil || got.ReapedFromPhase != "" {
		t.Errorf("reaped record not cleared by claim: %v %q", got.ReapedFromShem, got.ReapedFromPhase)
	}
}

// A GitHub ticket whose issue body changed after approval is unclaimable
// once it is back in the pool; reclaiming must honour the same gate.
func TestReclaim_RefusesAnUnapprovedBody(t *testing.T) {
	h, mux := setupTicketTest(t)
	a := seedShem(t, h, "shem-a", "key-a")
	ticket := seedReapedTicket(t, h, a, "implement")
	issue := 42
	h.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(map[string]any{
		"issue_number": issue, "intake_approved": true,
		"approved_body_hash": "old", "body_hash": "new",
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/reclaim", "shem-a", "key-a"))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", w.Code)
	}
}

// A human stop must hold against the shem still running the reaped work:
// stop, then resume (back to unassigned), must not make it reclaimable.
func TestReclaim_RefusedAfterStopAndResume(t *testing.T) {
	h, mux := setupTicketTest(t)
	a := seedShem(t, h, "shem-a", "key-a")
	_, cookie := seedSessionUser(t, h.DB, "operator", string(rbac.RoleAdmin))
	ticket := seedReapedTicket(t, h, a, "implement")

	if code := postAction(t, mux, cookie, ticket.ID, "stop"); code != http.StatusNoContent {
		t.Fatalf("stop = %d, want 204", code)
	}
	if code := postAction(t, mux, cookie, ticket.ID, "resume"); code != http.StatusNoContent {
		t.Fatalf("resume = %d, want 204", code)
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, shemRequest(http.MethodPost, "/api/tickets/"+ticket.ID+"/reclaim", "shem-a", "key-a"))
	if w.Code != http.StatusConflict {
		t.Fatalf("reclaim after stop+resume = %d, want 409", w.Code)
	}
}
