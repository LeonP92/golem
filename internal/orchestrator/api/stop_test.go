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
	// pending-approval is in this list because the intake gate's invariant
	// is that approval is the only way out of it; a stop would be a second
	// exit, and there is nothing running to interrupt anyway.
	for _, phase := range []string{"closed", "stopped", "pending-approval"} {
		tk := db.Ticket{RepoRemote: "r", Branch: "b" + phase, Description: "d", Phase: phase}
		if err := h.DB.Create(&tk).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
		if code := postAction(t, mux, cookie, tk.ID, "stop"); code == http.StatusNoContent {
			t.Errorf("stop succeeded on a %s ticket", phase)
		}
	}
}

// A shem's phase report must not undo a stop.
//
// Review finding 2, verified by the reviewer as executed: stop sets
// phase=stopped but leaves assigned_shem set, and updatePhase's WHERE was
// only `id = ? AND assigned_shem = ?`. A PostPhase already in flight — the
// shem finishing the phase it was in when the stop arrived — then wrote
// straight over it and answered 204. The ticket came back assigned, idle
// and resumable, which is the exact opposite of what stop.go documents as
// its invariant.
//
// The race is real rather than theoretical: cancelling the agent is
// asynchronous, so a phase report is very likely to be in flight at the
// moment a human clicks stop.
func TestStoppedTicketRejectsShemPhaseUpdates(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "racing-shem", "racingkey")
	_, cookie := seedSessionUser(t, h.DB, "operator", string(rbac.RoleAdmin))

	tk := db.Ticket{
		RepoRemote: "https://github.com/org/repo", Branch: "ticket/x-abc12345",
		BaseBranch: "main", Description: "d", Phase: "brainstorm", AssignedShem: &shem.ID,
	}
	if err := h.DB.Create(&tk).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if code := postAction(t, mux, cookie, tk.ID, "stop"); code != http.StatusNoContent {
		t.Fatalf("stop = %d", code)
	}

	// The shem, unaware, reports the phase it had moved on to.
	body, _ := json.Marshal(map[string]string{"phase": "plan"})
	req := httptest.NewRequest(http.MethodPatch,
		fmt.Sprintf("/api/tickets/%s/phase", tk.ID), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer racingkey")
	req.Header.Set("X-Shem-Name", "racing-shem")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code == http.StatusNoContent {
		t.Error("the shem's phase update was accepted on a stopped ticket")
	}
	var after db.Ticket
	h.DB.First(&after, "id = ?", tk.ID)
	if after.Phase != "stopped" {
		t.Errorf("phase = %q, want stopped: the shem overwrote a human's stop and the "+
			"ticket is assigned, idle and resumable again", after.Phase)
	}
}
