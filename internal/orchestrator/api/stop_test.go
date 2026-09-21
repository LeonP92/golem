package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/rbac"
)

func postAction(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, id, action string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": action})
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/tickets/%s/actions", id), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(cookie.Value))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w.Code
}

// A human can interrupt a shem at any point, and the ticket then stays
// stopped until a human starts it again.
//
// "Stays stopped" is the whole feature. Every other phase in this system is
// something an automatic mechanism will eventually pick up — the poll loop
// claims unassigned work, resumableTickets recovers a dead shem's run, the
// shem takes back anything in revising, the pull-request monitor dispatches
// on a failing check. A stop that any of those could undo is not a stop.
func TestStopAndResume(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "stop-shem", "stopkey")
	_, cookie := seedSessionUser(t, h.DB, "operator", string(rbac.RoleAdmin))

	phase := "implement"
	tk := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Branch: "ticket/x-abc12345",
		BaseBranch: "main", Description: "d", Phase: "implement",
		AssignedShem: &shem.ID, CheckpointPhase: &phase,
	}
	if err := h.DB.Create(&tk).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	if code := postAction(t, mux, cookie, tk.ID, "stop"); code != http.StatusNoContent {
		t.Fatalf("stop = %d, want 204", code)
	}
	var stopped db.Ticket
	h.DB.First(&stopped, "id = ?", tk.ID)
	if stopped.Phase != "stopped" {
		t.Fatalf("phase = %q, want stopped", stopped.Phase)
	}
	if stopped.StoppedFromPhase != "implement" {
		t.Errorf("stopped_from_phase = %q, want implement: without it, starting again "+
			"cannot put the ticket back where it was interrupted", stopped.StoppedFromPhase)
	}

	// Nothing automatic may take it back.
	req := httptest.NewRequest(http.MethodGet, "/api/tickets/resumable", nil)
	req.Header.Set("Authorization", "Bearer stopkey")
	req.Header.Set("X-Shem-Name", "stop-shem")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var claims []api.ClaimResponse
	json.Unmarshal(w.Body.Bytes(), &claims) //nolint:errcheck
	for _, c := range claims {
		if c.TicketID == tk.ID {
			t.Error("a stopped ticket was offered for resume; the shem would start it " +
				"again on its next restart and the stop would not hold")
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tickets/available?repo=https://github.com/org/repo", nil)
	req.Header.Set("Authorization", "Bearer stopkey")
	req.Header.Set("X-Shem-Name", "stop-shem")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if bytes.Contains(w.Body.Bytes(), []byte(tk.ID)) {
		t.Error("a stopped ticket was offered as available work")
	}

	// Only a human brings it back, and it returns where it left off.
	if code := postAction(t, mux, cookie, tk.ID, "resume"); code != http.StatusNoContent {
		t.Fatalf("resume = %d, want 204", code)
	}
	var resumed db.Ticket
	h.DB.First(&resumed, "id = ?", tk.ID)
	if resumed.Phase != "implement" {
		t.Errorf("phase = %q, want implement: resume must restore the interrupted phase", resumed.Phase)
	}
	if resumed.StoppedFromPhase != "" {
		t.Errorf("stopped_from_phase = %q, want cleared after resume", resumed.StoppedFromPhase)
	}
}

// Resume is only meaningful from stopped. Allowing it from anywhere would
// make it a way to rewrite a running ticket's phase from the UI.
func TestResumeOnlyFromStopped(t *testing.T) {
	h, mux := setupTicketTest(t)
	_, cookie := seedSessionUser(t, h.DB, "operator", string(rbac.RoleAdmin))
	tk := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	if err := h.DB.Create(&tk).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if code := postAction(t, mux, cookie, tk.ID, "resume"); code == http.StatusNoContent {
		t.Error("resume succeeded on a ticket that was not stopped")
	}
}

// Stopping a ticket that is already finished is meaningless and must not
// resurrect it into a phase a human then has to clear.
func TestStopRejectsTerminalPhases(t *testing.T) {
	h, mux := setupTicketTest(t)
	_, cookie := seedSessionUser(t, h.DB, "operator", string(rbac.RoleAdmin))
	for _, phase := range []string{"closed", "stopped"} {
		tk := db.Ticket{RepoRemote: "r", Branch: "b" + phase, Description: "d", Phase: phase}
		if err := h.DB.Create(&tk).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
		if code := postAction(t, mux, cookie, tk.ID, "stop"); code == http.StatusNoContent {
			t.Errorf("stop succeeded on a %s ticket", phase)
		}
	}
}
