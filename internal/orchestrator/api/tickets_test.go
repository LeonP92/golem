package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func setupTicketTest(t *testing.T) (*api.Handlers, *http.ServeMux) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hub := ws.NewHub()
	h := api.NewHandlers(gdb, hub, sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterShemRoutes(mux)
	h.RegisterTicketRoutes(mux)
	h.RegisterLogRoutes(mux)
	h.RegisterHumanRoutes(mux)
	return h, mux
}

// seedSessionUser creates a db.User and an authenticated session cookie for it.
func seedSessionUser(t *testing.T, gdb *gorm.DB, username string) (db.User, *http.Cookie) {
	t.Helper()
	user := db.User{Username: username, PasswordHash: "x"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	w := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, w, user.ID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return user, w.Result().Cookies()[0]
}

// seedShem creates a Shem with a known API key and returns the key.
func seedShem(t *testing.T, h *api.Handlers, name, key string) db.Shem {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	shem := db.Shem{Name: name, APIKeyHash: string(hash), Repos: "[]", Status: "offline"}
	h.DB.Create(&shem)
	return shem
}

func TestAvailableTickets(t *testing.T) {
	h, mux := setupTicketTest(t)

	// Seed a shem so RequireAPIKey works.
	seedShem(t, h, "shem-a", "testkey")

	// Seed an unassigned ticket.
	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/available",
		Description: "available test",
		Phase:       "unassigned",
	}
	h.DB.Create(&ticket)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets/available?repo=https://github.com/org/repo", nil)
	req.Header.Set("Authorization", "Bearer testkey")
	req.Header.Set("X-Shem-Name", "shem-a")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var tickets []db.Ticket
	if err := json.Unmarshal(w.Body.Bytes(), &tickets); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(tickets) != 1 {
		t.Errorf("expected 1 ticket, got %d", len(tickets))
	}
}

func TestClaimTicket_HTTPEndpoint(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "claimer", "claimkey")

	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo2",
		Branch:      "ticket/claim",
		Description: "claim test",
		Phase:       "unassigned",
	}
	h.DB.Create(&ticket)

	url := fmt.Sprintf("/api/tickets/%s/claim", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer claimkey")
	req.Header.Set("X-Shem-Name", "claimer")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify ticket is now claimed.
	var updated db.Ticket
	h.DB.First(&updated, "id = ?", ticket.ID)
	if updated.Phase != "claimed" {
		t.Errorf("expected phase=claimed, got %s", updated.Phase)
	}
	if updated.AssignedShem == nil || *updated.AssignedShem != shem.ID {
		t.Errorf("expected assigned_shem=%d, got %v", shem.ID, updated.AssignedShem)
	}
}

func TestUpdatePhase(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "phase-shem", "phasekey")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo3",
		Branch:       "ticket/phase",
		Description:  "phase test",
		Phase:        "claimed",
		AssignedShem: &shem.ID,
	}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{"phase": "in-progress"})
	url := fmt.Sprintf("/api/tickets/%s/phase", ticket.ID)
	req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer phasekey")
	req.Header.Set("X-Shem-Name", "phase-shem")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReviseClaim_Success(t *testing.T) {
	h, _ := setupTicketTest(t)
	shem := seedShem(t, h, "revise-shem", "revisekey")

	sha := "abc123"
	ticket := db.Ticket{
		RepoRemote:    "https://github.com/org/repo-revise",
		Branch:        "ticket/revise",
		Description:   "revise test",
		Phase:         "revising",
		AssignedShem:  &shem.ID,
		CheckpointSHA: &sha,
	}
	h.DB.Create(&ticket)

	resp, err := h.ReviseClaim(ticket.ID, shem.ID)
	if err != nil {
		t.Fatalf("ReviseClaim: %v", err)
	}
	if resp.CheckpointPhase == nil || *resp.CheckpointPhase != "revising" {
		t.Errorf("expected checkpoint_phase=revising, got %v", resp.CheckpointPhase)
	}
	if resp.CheckpointSHA == nil || *resp.CheckpointSHA != sha {
		t.Errorf("expected checkpoint_sha=%s, got %v", sha, resp.CheckpointSHA)
	}
}

func TestReviseClaim_WrongShem_Conflict(t *testing.T) {
	h, _ := setupTicketTest(t)
	owner := seedShem(t, h, "owner-shem", "ownerkey")
	other := seedShem(t, h, "other-shem", "otherkey")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo-revise2",
		Branch:       "ticket/revise2",
		Description:  "revise test 2",
		Phase:        "revising",
		AssignedShem: &owner.ID,
	}
	h.DB.Create(&ticket)

	if _, err := h.ReviseClaim(ticket.ID, other.ID); err == nil {
		t.Fatal("expected error for wrong shem, got nil")
	}
}

func TestReviseClaim_WrongPhase_Conflict(t *testing.T) {
	h, _ := setupTicketTest(t)
	shem := seedShem(t, h, "revise-shem-3", "revisekey3")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo-revise3",
		Branch:       "ticket/revise3",
		Description:  "revise test 3",
		Phase:        "ready-for-review",
		AssignedShem: &shem.ID,
	}
	h.DB.Create(&ticket)

	if _, err := h.ReviseClaim(ticket.ID, shem.ID); err == nil {
		t.Fatal("expected error for wrong phase, got nil")
	}
}

func TestReviseClaim_HTTPEndpoint(t *testing.T) {
	h, mux := setupTicketTest(t)
	shem := seedShem(t, h, "revise-shem-4", "revisekey4")

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo-revise4",
		Branch:       "ticket/revise4",
		Description:  "revise test 4",
		Phase:        "revising",
		AssignedShem: &shem.ID,
	}
	h.DB.Create(&ticket)

	url := fmt.Sprintf("/api/tickets/%s/revise-claim", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer revisekey4")
	req.Header.Set("X-Shem-Name", "revise-shem-4")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp api.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CheckpointPhase == nil || *resp.CheckpointPhase != "revising" {
		t.Errorf("expected checkpoint_phase=revising, got %v", resp.CheckpointPhase)
	}
}

func TestAppendLog(t *testing.T) {
	h, mux := setupTicketTest(t)
	seedShem(t, h, "log-shem", "logkey")

	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo4",
		Branch:      "ticket/log",
		Description: "log test",
		Phase:       "in-progress",
	}
	h.DB.Create(&ticket)

	body, _ := json.Marshal(map[string]string{
		"entry_type": "message",
		"message":    "hello from shem",
		"from_role":  "developer",
	})
	url := fmt.Sprintf("/api/tickets/%s/log", ticket.ID)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer logkey")
	req.Header.Set("X-Shem-Name", "log-shem")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]uint
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["sequence_num"] != 1 {
		t.Errorf("expected sequence_num=1, got %d", resp["sequence_num"])
	}
}

func TestCreateTicket_SetsCreatedByFromSession(t *testing.T) {
	h, mux := setupTicketTest(t)
	user, cookie := seedSessionUser(t, h.DB, "leon")

	body, _ := json.Marshal(map[string]string{
		"repo_remote": "https://github.com/org/repo5",
		"branch":      "ticket/created-by",
		"description": "created by test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", bytes.NewReader(body))
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		CreatedByUserID *uint  `json:"created_by_user_id"`
		CreatedBy       string `json:"created_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CreatedByUserID == nil || *resp.CreatedByUserID != user.ID {
		t.Errorf("expected created_by_user_id=%d, got %v", user.ID, resp.CreatedByUserID)
	}
	if resp.CreatedBy != "leon" {
		t.Errorf("expected created_by=leon, got %q", resp.CreatedBy)
	}
}

func TestCreateTicket_IgnoresClientSuppliedCreatedByUserID(t *testing.T) {
	h, mux := setupTicketTest(t)
	user, cookie := seedSessionUser(t, h.DB, "leon")

	body, _ := json.Marshal(map[string]any{
		"repo_remote":        "https://github.com/org/repo6",
		"branch":             "ticket/spoof",
		"description":        "spoof test",
		"created_by_user_id": user.ID + 999,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/tickets", bytes.NewReader(body))
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		CreatedByUserID *uint `json:"created_by_user_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CreatedByUserID == nil || *resp.CreatedByUserID != user.ID {
		t.Errorf("expected created_by_user_id to be the session user %d, got %v", user.ID, resp.CreatedByUserID)
	}
}

func TestListAndGetTicket_ReturnsCreatedBy(t *testing.T) {
	h, mux := setupTicketTest(t)
	user, cookie := seedSessionUser(t, h.DB, "leon")

	owned := db.Ticket{
		RepoRemote:      "https://github.com/org/repo7",
		Branch:          "ticket/owned",
		Description:     "owned ticket",
		Phase:           "unassigned",
		CreatedByUserID: &user.ID,
	}
	h.DB.Create(&owned)
	legacy := db.Ticket{
		RepoRemote:  "https://github.com/org/repo8",
		Branch:      "ticket/legacy",
		Description: "legacy ticket",
		Phase:       "unassigned",
	}
	h.DB.Create(&legacy)

	req := httptest.NewRequest(http.MethodGet, "/api/tickets", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var list []struct {
		ID        string `json:"id"`
		CreatedBy string `json:"created_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := make(map[string]string, len(list))
	for _, t := range list {
		byID[t.ID] = t.CreatedBy
	}
	if byID[owned.ID] != "leon" {
		t.Errorf("expected owned ticket created_by=leon, got %q", byID[owned.ID])
	}
	if byID[legacy.ID] != "" {
		t.Errorf("expected legacy ticket created_by empty, got %q", byID[legacy.ID])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/tickets/"+owned.ID, nil)
	req.AddCookie(cookie)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var single struct {
		Ticket struct {
			CreatedBy string `json:"created_by"`
		} `json:"ticket"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &single); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if single.Ticket.CreatedBy != "leon" {
		t.Errorf("expected created_by=leon, got %q", single.Ticket.CreatedBy)
	}
}
