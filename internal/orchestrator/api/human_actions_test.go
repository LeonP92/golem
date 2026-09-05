package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// setupAPIKeyTest creates a db, handlers, mux with human routes, and returns a
// shem API key that passes RequireAPIKey.
func setupAPIKeyTest(t *testing.T) (*api.Handlers, *http.ServeMux, string) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)

	apiKey := "test-shem-key"
	hash, _ := bcrypt.GenerateFromPassword([]byte(apiKey), bcrypt.MinCost)
	shem := db.Shem{Name: "test-shem", APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	gdb.Create(&shem)
	return h, mux, apiKey
}

func makeSessionCookie(t *testing.T, h *api.Handlers, userID uint) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(h.DB, rec, userID); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no session cookie returned")
	}
	return cookies[0]
}

// setupActionTest creates a db, handlers, mux with human routes registered, and returns a session cookie.
func setupActionTest(t *testing.T) (*api.Handlers, *http.ServeMux, *http.Cookie) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)

	user := db.User{Username: "testadmin", PasswordHash: "x"}
	gdb.Create(&user)
	cookie := makeSessionCookie(t, h, user.ID)
	return h, mux, cookie
}

// TestApproveTicket_TransitionsPhase verifies that POST /api/tickets/{id}/approve
// resolves the pending approval HumanInput and advances the ticket phase.
func TestApproveTicket_TransitionsPhase(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "brainstorm"}
	h.DB.Create(&ticket)
	hi := db.HumanInput{TicketID: ticket.ID, Kind: "approval", Prompt: "approve?"}
	h.DB.Create(&hi)

	body, _ := json.Marshal(map[string]string{"action": "approve"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "plan" {
		t.Errorf("expected phase=plan, got %q", got.Phase)
	}

	var resolvedHI db.HumanInput
	h.DB.First(&resolvedHI, hi.ID)
	if resolvedHI.ResolvedAt == nil {
		t.Error("expected human_input to be resolved")
	}
	if resolvedHI.Response == nil || *resolvedHI.Response != "approved" {
		t.Errorf("expected response='approved', got %v", resolvedHI.Response)
	}
}

// TestApproveTicket_PlanToImplement verifies brainstorm→plan, plan→implement transitions.
func TestApproveTicket_PlanToImplement(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "plan"}
	h.DB.Create(&ticket)
	hi := db.HumanInput{TicketID: ticket.ID, Kind: "approval", Prompt: "approve plan?"}
	h.DB.Create(&hi)

	body, _ := json.Marshal(map[string]string{"action": "approve"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "implement" {
		t.Errorf("expected phase=implement, got %q", got.Phase)
	}
}

// TestApproveTicket_WrongPhase verifies 400 when ticket is not in an approvable phase.
func TestApproveTicket_WrongPhase(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "approve"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestAnswerHumanInput_ResolvesAndLogs verifies that POST /api/tickets/{id}/answer
// resolves the HumanInput and writes a log entry.
func TestAnswerHumanInput_ResolvesAndLogs(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	h.DB.Create(&ticket)
	hi := db.HumanInput{TicketID: ticket.ID, Kind: "question_answer", Prompt: "which way?"}
	h.DB.Create(&hi)

	body, _ := json.Marshal(map[string]interface{}{
		"action":   "answer",
		"input_id": hi.ID,
		"response": "go left",
	})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var resolvedHI db.HumanInput
	h.DB.First(&resolvedHI, hi.ID)
	if resolvedHI.ResolvedAt == nil {
		t.Error("expected resolved_at to be set")
	}
	if resolvedHI.Response == nil || *resolvedHI.Response != "go left" {
		t.Errorf("expected response='go left', got %v", resolvedHI.Response)
	}

	var logs []db.LogEntry
	h.DB.Where("ticket_id = ?", ticket.ID).Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logs))
	}
	if logs[0].EntryType != "ANSWER" || logs[0].FromRole != "human" {
		t.Errorf("unexpected log: %+v", logs[0])
	}
}

// TestAnswerHumanInput_MissingFields verifies 400 when input_id or response missing.
func TestAnswerHumanInput_MissingFields(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]interface{}{
		"action":   "answer",
		"input_id": 0,
		"response": "",
	})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestRequeueTicket_TransitionsToUnassigned verifies that a needs-attention ticket
// is moved to unassigned and a HUMAN_FEEDBACK log entry is written.
func TestRequeueTicket_TransitionsToUnassigned(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	shemID := uint(99)
	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "needs-attention", AssignedShem: &shemID}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "requeue", "feedback": "try harder"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "unassigned" {
		t.Errorf("expected phase=unassigned, got %q", got.Phase)
	}
	if got.AssignedShem != nil {
		t.Errorf("expected assigned_shem=nil, got %v", got.AssignedShem)
	}

	var logs []db.LogEntry
	h.DB.Where("ticket_id = ?", ticket.ID).Find(&logs)
	if len(logs) != 1 || logs[0].EntryType != "HUMAN_FEEDBACK" {
		t.Errorf("expected 1 HUMAN_FEEDBACK log, got %+v", logs)
	}
}

// TestRequeueTicket_WrongPhase verifies 409 when ticket is already unassigned or closed.
func TestRequeueTicket_WrongPhase(t *testing.T) {
	for _, phase := range []string{"unassigned", "closed"} {
		t.Run(phase, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)

			ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: phase}
			h.DB.Create(&ticket)

			b, _ := json.Marshal(map[string]string{"action": "requeue"})
			url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusConflict {
				t.Errorf("phase=%s: expected 409, got %d", phase, w.Code)
			}
		})
	}
}

// TestRequeueTicket_AnyActivePhase verifies requeue works for any non-terminal phase.
func TestRequeueTicket_AnyActivePhase(t *testing.T) {
	for _, phase := range []string{"claimed", "brainstorm", "plan", "implement", "needs-attention"} {
		t.Run(phase, func(t *testing.T) {
			h, mux, cookie := setupActionTest(t)

			ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: phase}
			h.DB.Create(&ticket)

			b, _ := json.Marshal(map[string]string{"action": "requeue"})
			url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
			req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(cookie)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusNoContent {
				t.Errorf("phase=%s: expected 204, got %d: %s", phase, w.Code, w.Body.String())
			}
		})
	}
}

// TestCloseTicket_TransitionsToClosed verifies ready-for-review → closed.
func TestCloseTicket_TransitionsToClosed(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "ready-for-review"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "close"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "closed" {
		t.Errorf("expected phase=closed, got %q", got.Phase)
	}
}

// TestCloseTicket_WrongPhase verifies 409 when not in ready-for-review.
func TestCloseTicket_WrongPhase(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "close"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

// TestNeedsAttentionTicket verifies POST /api/tickets/{id}/needs-attention.
func TestNeedsAttentionTicket(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "needs-attention"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "needs-attention" {
		t.Errorf("expected phase=needs-attention, got %q", got.Phase)
	}
}

// TestRequestApproval_CreatesHumanInput verifies POST /api/tickets/{id}/request-approval
// creates a HumanInput with kind=approval.
func TestRequestApproval_CreatesHumanInput(t *testing.T) {
	h, mux, apiKey := setupAPIKeyTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "brainstorm"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"kind": "approval", "prompt": "Please review the spec."})
	url := fmt.Sprintf("/api/tickets/%s/human-inputs", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var hi db.HumanInput
	if err := h.DB.Where("ticket_id = ? AND kind = 'approval'", ticket.ID).First(&hi).Error; err != nil {
		t.Fatalf("HumanInput not created: %v", err)
	}
	if hi.Prompt != "Please review the spec." {
		t.Errorf("expected prompt %q, got %q", "Please review the spec.", hi.Prompt)
	}
	if hi.ResolvedAt != nil {
		t.Errorf("expected unresolved, got resolved_at=%v", hi.ResolvedAt)
	}
}

// TestRequestApproval_EmptyPrompt verifies 400 when prompt is empty.
func TestRequestApproval_EmptyPrompt(t *testing.T) {
	_, mux, apiKey := setupAPIKeyTest(t)

	body, _ := json.Marshal(map[string]string{"kind": "approval", "prompt": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets/some-uuid-here/human-inputs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestPendingApproval_ReturnsPending verifies GET /api/tickets/{id}/human-inputs?kind=approval&resolved=false
// returns unresolved approval HumanInputs.
func TestPendingApproval_ReturnsPending(t *testing.T) {
	h, mux, apiKey := setupAPIKeyTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "brainstorm"}
	h.DB.Create(&ticket)
	hi := db.HumanInput{TicketID: ticket.ID, Kind: "approval", Prompt: "Review spec"}
	h.DB.Create(&hi)

	url := fmt.Sprintf("/api/tickets/%s/human-inputs?kind=approval&resolved=false", ticket.ID)
	req := httptest.NewRequest(http.MethodGet, url, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var results []db.HumanInput
	if err := json.NewDecoder(w.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) == 0 || results[0].Kind != "approval" {
		t.Errorf("expected approval result, got %+v", results)
	}
}

// TestPendingApproval_NoneReturnsEmpty verifies empty array when no pending approval exists.
func TestPendingApproval_NoneReturnsEmpty(t *testing.T) {
	h, mux, apiKey := setupAPIKeyTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "brainstorm"}
	h.DB.Create(&ticket)

	url := fmt.Sprintf("/api/tickets/%s/human-inputs?kind=approval&resolved=false", ticket.ID)
	req := httptest.NewRequest(http.MethodGet, url, http.NoBody)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var results []db.HumanInput
	if err := json.NewDecoder(w.Body).Decode(&results); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected empty array, got %d results", len(results))
	}
}
