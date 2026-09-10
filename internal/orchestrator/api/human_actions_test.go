package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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
	if err := auth.CreateSession(h.DB, rec, userID, false); err != nil {
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
	h, mux, _, cookie := setupActionTestWithHub(t)
	return h, mux, cookie
}

// setupActionTestWithHub is like setupActionTest but also exposes the Hub, so
// tests can register a fake shem connection and assert on pushed messages.
func setupActionTestWithHub(t *testing.T) (*api.Handlers, *http.ServeMux, *ws.Hub, *http.Cookie) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hub := ws.NewHub()
	h := api.NewHandlers(gdb, hub, sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)

	user := db.User{Username: "testadmin", PasswordHash: "x"}
	gdb.Create(&user)
	cookie := makeSessionCookie(t, h, user.ID)
	return h, mux, hub, cookie
}

// connectFakeShem registers a real WebSocket connection with the hub for shemID,
// so tests can assert on messages pushed via Hub.Push.
func connectFakeShem(t *testing.T, hub *ws.Hub, shemID uint) *websocket.Conn {
	t.Helper()
	registered := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		hub.Register(shemID, conn)
		close(registered)
	}))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	<-registered
	return client
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

// TestCloseTicket_WrongPhase verifies 409 when ticket is already closed.
func TestCloseTicket_WrongPhase(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "closed"}
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
	req.Header.Set("X-Shem-Name", "test-shem")
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
	req.Header.Set("X-Shem-Name", "test-shem")
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
	req.Header.Set("X-Shem-Name", "test-shem")
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

// TestActionRequestChanges_FromReadyForReview_MovesToRevisingAndPushes verifies
// that request-changes on a ready-for-review ticket moves it to revising,
// records feedback, and pushes a ticket_revise message to the assigned shem.
func TestActionRequestChanges_FromReadyForReview_MovesToRevisingAndPushes(t *testing.T) {
	h, mux, hub, cookie := setupActionTestWithHub(t)

	shemID := uint(42)
	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Branch:       "b",
		Description:  "d",
		Phase:        "ready-for-review",
		AssignedShem: &shemID,
	}
	h.DB.Create(&ticket)

	conn := connectFakeShem(t, hub, shemID)

	body, _ := json.Marshal(map[string]string{"action": "request-changes", "feedback": "please fix the bug"})
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
	if got.Phase != "revising" {
		t.Errorf("expected phase=revising, got %q", got.Phase)
	}

	var fb db.HumanInput
	if err := h.DB.Where("ticket_id = ? AND kind = 'feedback'", ticket.ID).First(&fb).Error; err != nil {
		t.Fatalf("feedback HumanInput not created: %v", err)
	}
	if fb.Prompt != "please fix the bug" {
		t.Errorf("expected prompt %q, got %q", "please fix the bug", fb.Prompt)
	}

	var logs []db.LogEntry
	h.DB.Where("ticket_id = ? AND entry_type = 'HUMAN_FEEDBACK'", ticket.ID).Find(&logs)
	if len(logs) != 1 {
		t.Fatalf("expected 1 HUMAN_FEEDBACK log, got %d", len(logs))
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected ticket_revise push, got error: %v", err)
	}
	var msg ws.WSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal push: %v", err)
	}
	if msg.Type != "ticket_revise" {
		t.Errorf("expected type=ticket_revise, got %q", msg.Type)
	}
	if msg.TicketID == nil || *msg.TicketID != ticket.ID {
		t.Errorf("expected ticket_id=%s, got %v", ticket.ID, msg.TicketID)
	}
}

// TestActionRequestChanges_FromReadyForReview_NoAssignedShem_Conflict verifies
// 409 when the ticket has no assigned shem to wake up.
func TestActionRequestChanges_FromReadyForReview_NoAssignedShem_Conflict(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "ready-for-review"}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "request-changes", "feedback": "fix it"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "ready-for-review" {
		t.Errorf("expected phase unchanged, got %q", got.Phase)
	}
}

// TestActionRequestChanges_AlreadyRevising_FallsThroughTo404 verifies that a
// ticket already moved to revising (e.g. a second request-changes submitted
// before the first one's UI reloaded) does not re-enter the ready-for-review
// branch — it falls through to the legacy approval-lookup branch, which 404s
// since there's no pending approval, and the phase is left unchanged.
func TestActionRequestChanges_AlreadyRevising_FallsThroughTo404(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	shemID := uint(7)
	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "revising", AssignedShem: &shemID}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"action": "request-changes", "feedback": "fix it"})
	url := fmt.Sprintf("/api/tickets/%s/actions", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	// ticket.Phase == "revising" here, so actionRequestChanges falls through to
	// the brainstorm/plan branch, which 404s because there's no pending approval.
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}

	var got db.Ticket
	h.DB.First(&got, "id = ?", ticket.ID)
	if got.Phase != "revising" {
		t.Errorf("expected phase unchanged, got %q", got.Phase)
	}
}

// TestActionRequestChanges_Brainstorm_ResolvesApprovalNoPhaseChange verifies the
// existing brainstorm/plan request-changes behavior is unchanged: it resolves
// the pending approval, records feedback, and does not touch ticket.Phase.
func TestActionRequestChanges_Brainstorm_ResolvesApprovalNoPhaseChange(t *testing.T) {
	h, mux, cookie := setupActionTest(t)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "brainstorm"}
	h.DB.Create(&ticket)
	hi := db.HumanInput{TicketID: ticket.ID, Kind: "approval", Prompt: "approve?"}
	h.DB.Create(&hi)

	body, _ := json.Marshal(map[string]string{"action": "request-changes", "feedback": "needs more detail"})
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
	if got.Phase != "brainstorm" {
		t.Errorf("expected phase unchanged (brainstorm), got %q", got.Phase)
	}

	var resolvedHI db.HumanInput
	h.DB.First(&resolvedHI, hi.ID)
	if resolvedHI.ResolvedAt == nil {
		t.Error("expected approval to be resolved")
	}
	if resolvedHI.Response == nil || *resolvedHI.Response != "changes_requested" {
		t.Errorf("expected response='changes_requested', got %v", resolvedHI.Response)
	}

	var fb db.HumanInput
	if err := h.DB.Where("ticket_id = ? AND kind = 'feedback'", ticket.ID).First(&fb).Error; err != nil {
		t.Fatalf("feedback HumanInput not created: %v", err)
	}
	if fb.Prompt != "needs more detail" {
		t.Errorf("expected prompt %q, got %q", "needs more detail", fb.Prompt)
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
	req.Header.Set("X-Shem-Name", "test-shem")
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
