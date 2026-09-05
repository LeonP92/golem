package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"golang.org/x/crypto/bcrypt"
)

func setupHumanTest(t *testing.T) (*api.Handlers, *http.ServeMux, db.Ticket) {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)

	ticket := db.Ticket{RepoRemote: "r", Branch: "b", Description: "d", Phase: "implement"}
	gdb.Create(&ticket)

	return h, mux, ticket
}

// seedShemForHuman creates a Shem with a known API key for Shem-facing endpoint tests.
func seedShemForHuman(t *testing.T, h *api.Handlers, name, key string) {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte(key), bcrypt.MinCost)
	shem := db.Shem{Name: name, APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	h.DB.Create(&shem)
}

func TestPendingHumanInput_ReturnsOldest(t *testing.T) {
	h, mux, ticket := setupHumanTest(t)

	// Seed a shem with an API key for auth.
	seedShemForHuman(t, h, "shem-human-1", "humankey1")

	now := time.Now()
	// Seed two unresolved inputs (oldest first).
	old := db.HumanInput{
		TicketID:  ticket.ID,
		Kind:      "question_answer",
		Prompt:    "first question",
		CreatedAt: now.Add(-time.Minute),
	}
	h.DB.Create(&old)
	newer := db.HumanInput{
		TicketID:  ticket.ID,
		Kind:      "approval",
		Prompt:    "approve this?",
		CreatedAt: now,
	}
	h.DB.Create(&newer)
	// Seed one resolved input — must not appear in results.
	resolved := db.HumanInput{
		TicketID:   ticket.ID,
		Kind:       "question_answer",
		Prompt:     "already answered",
		CreatedAt:  now.Add(-2 * time.Minute),
		ResolvedAt: func() *time.Time { t := now.Add(-time.Minute); return &t }(),
	}
	h.DB.Create(&resolved)

	url := fmt.Sprintf("/api/tickets/%s/human-inputs?resolved=false", ticket.ID)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer humankey1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got []db.HumanInput
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 unresolved inputs, got %d: %+v", len(got), got)
	}
	if got[0].ID != old.ID {
		t.Errorf("expected oldest input ID=%d first, got ID=%d", old.ID, got[0].ID)
	}
}

func TestAckHumanInput(t *testing.T) {
	h, mux, ticket := setupHumanTest(t)

	// Seed a shem with an API key for auth.
	seedShemForHuman(t, h, "shem-human-2", "humankey2")

	input := db.HumanInput{
		TicketID:  ticket.ID,
		Kind:      "question_answer",
		Prompt:    "do it?",
		CreatedAt: time.Now(),
	}
	h.DB.Create(&input)

	body, _ := json.Marshal(map[string]string{"response": "yes"})
	url := fmt.Sprintf("/api/tickets/%s/human-inputs/%d", ticket.ID, input.ID)
	req := httptest.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer humankey2")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify resolved.
	var updated db.HumanInput
	h.DB.First(&updated, input.ID)
	if updated.ResolvedAt == nil {
		t.Error("expected resolved_at to be set")
	}
	if updated.Response == nil || *updated.Response != "yes" {
		t.Errorf("expected response='yes', got %v", updated.Response)
	}
}
