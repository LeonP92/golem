package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func TestRegisterShem(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("key1"), bcrypt.MinCost)
	shem := db.Shem{Name: "node-a", APIKeyHash: string(hash), Repos: "[]", Status: "offline"}
	gdb.Create(&shem)

	body, _ := json.Marshal(map[string]any{
		"name":  "node-a",
		"repos": []string{"https://github.com/org/repo"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/shems/register", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer key1")
	req.Header.Set("X-Shem-Name", "node-a")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	hub := ws.NewHub()
	h := api.NewHandlers(gdb, hub, nil)
	h.RegisterShemRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["shem_id"]; !ok {
		t.Errorf("expected shem_id in response, got %v", resp)
	}
}

func TestClaimTicket_AtomicOneWinner(t *testing.T) {
	gdb, err := db.Open("file::memory:?cache=shared&mode=memory")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	// Force single connection so all goroutines share the same in-memory DB.
	sqlDB, _ := gdb.DB()
	sqlDB.SetMaxOpenConns(1)
	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/1",
		Description: "test",
		Phase:       "unassigned",
	}
	gdb.Create(&ticket)

	wins := make(chan bool, 10)
	var wg sync.WaitGroup
	hub := ws.NewHub()
	h := api.NewHandlers(gdb, hub, nil)

	for i := uint(1); i <= 5; i++ {
		sid := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, claimErr := h.ClaimTicket(ticket.ID, sid)
			wins <- claimErr == nil
		}()
	}
	wg.Wait()
	close(wins)

	winCount := 0
	for won := range wins {
		if won {
			winCount++
		}
	}
	if winCount != 1 {
		t.Errorf("expected exactly 1 winner, got %d", winCount)
	}
}

func TestDeregisterShem(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("key2"), bcrypt.MinCost)
	shem := db.Shem{Name: "node-b", APIKeyHash: string(hash), Repos: "[]", Status: "online"}
	gdb.Create(&shem)

	req := httptest.NewRequest(http.MethodDelete, "/api/shems/me", nil)
	req.Header.Set("Authorization", "Bearer key2")
	req.Header.Set("X-Shem-Name", "node-b")
	w := httptest.NewRecorder()

	mux := http.NewServeMux()
	hub := ws.NewHub()
	h := api.NewHandlers(gdb, hub, nil)
	h.RegisterShemRoutes(mux)
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
}
