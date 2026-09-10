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
