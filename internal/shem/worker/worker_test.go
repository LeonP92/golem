package worker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/shem/worker"
)

func strPtr(v string) *string { return &v }

func TestWorker_ClaimsOnPush(t *testing.T) {
	claimed := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/claim"):
			// Extract ticket ID from path: /api/tickets/{id}/claim
			parts := strings.Split(r.URL.Path, "/")
			var id string
			for i, p := range parts {
				if p == "tickets" && i+1 < len(parts) {
					id = parts[i+1]
					break
				}
			}
			claimed <- id
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(client.ClaimResponse{ //nolint:errcheck
				TicketID:   id,
				Branch:     "ticket/test",
				RepoRemote: "r",
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/register"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Orchestrator: srv.URL,
		APIKey:       "k",
		Name:         "n",
		Repos: []config.RepoConfig{
			{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"},
		},
	}
	c := client.New(srv.URL, "k", "test-shem")
	// nil executor = don't actually run golem
	w := worker.New(cfg, c, nil)
	go w.Start()
	defer w.Shutdown()

	// Give Start() time to register
	time.Sleep(50 * time.Millisecond)

	// Simulate a WS push
	const ticketID = "uuid-42-test"
	w.HandleMessage(ws.WSMessage{Type: "ticket_available", TicketID: strPtr(ticketID), Repo: "r"})

	select {
	case id := <-claimed:
		if id != ticketID {
			t.Errorf("claimed ticket %q, want %q", id, ticketID)
		}
	case <-time.After(time.Second):
		t.Error("timeout: no claim made")
	}
}

func TestWorker_ReviseOnPush(t *testing.T) {
	revised := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revise-claim"):
			parts := strings.Split(r.URL.Path, "/")
			var id string
			for i, p := range parts {
				if p == "tickets" && i+1 < len(parts) {
					id = parts[i+1]
					break
				}
			}
			revised <- id
			revisingPhase := "revising"
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(client.ClaimResponse{ //nolint:errcheck
				TicketID:        id,
				Branch:          "ticket/test",
				RepoRemote:      "r",
				CheckpointPhase: &revisingPhase,
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/register"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Orchestrator: srv.URL,
		APIKey:       "k",
		Name:         "n",
		Repos: []config.RepoConfig{
			{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"},
		},
	}
	c := client.New(srv.URL, "k", "test-shem")
	w := worker.New(cfg, c, nil)
	go w.Start()
	defer w.Shutdown()

	time.Sleep(50 * time.Millisecond)

	const ticketID = "uuid-revise-test"
	w.HandleMessage(ws.WSMessage{Type: "ticket_revise", TicketID: strPtr(ticketID), Repo: "r"})

	select {
	case id := <-revised:
		if id != ticketID {
			t.Errorf("revise-claimed ticket %q, want %q", id, ticketID)
		}
	case <-time.After(time.Second):
		t.Error("timeout: no revise-claim made")
	}
}

func TestWorker_HandlesRevise409(t *testing.T) {
	reviseAttempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/revise-claim") {
			reviseAttempts++
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		if strings.Contains(r.URL.Path, "/register") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Orchestrator: srv.URL,
		APIKey:       "k",
		Name:         "n",
		Repos:        []config.RepoConfig{{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"}},
	}
	c := client.New(srv.URL, "k", "test-shem")
	w := worker.New(cfg, c, nil)
	go w.Start()
	defer w.Shutdown()

	time.Sleep(50 * time.Millisecond)

	w.HandleMessage(ws.WSMessage{Type: "ticket_revise", TicketID: strPtr("uuid-6")})

	time.Sleep(200 * time.Millisecond)

	if reviseAttempts != 1 {
		t.Errorf("expected 1 revise-claim attempt, got %d", reviseAttempts)
	}
}

func TestWorker_IgnoresNonAvailableMessages(t *testing.T) {
	claimed := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			claimed <- "claimed"
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.Contains(r.URL.Path, "/register") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Orchestrator: srv.URL,
		APIKey:       "k",
		Name:         "n",
		Repos:        []config.RepoConfig{{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"}},
	}
	c := client.New(srv.URL, "k", "test-shem")
	w := worker.New(cfg, c, nil)
	go w.Start()
	defer w.Shutdown()

	// A message with a different type should not trigger a claim
	w.HandleMessage(ws.WSMessage{Type: "some_other_type", TicketID: strPtr("uuid-99")})

	select {
	case <-claimed:
		t.Error("should not have claimed for a non ticket_available message")
	case <-time.After(200 * time.Millisecond):
		// expected: no claim
	}
}

func TestWorker_HandlesClaim409(t *testing.T) {
	claimAttempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") {
			claimAttempts++
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		if strings.Contains(r.URL.Path, "/register") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfg := &config.Config{
		Orchestrator: srv.URL,
		APIKey:       "k",
		Name:         "n",
		Repos:        []config.RepoConfig{{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"}},
	}
	c := client.New(srv.URL, "k", "test-shem")
	w := worker.New(cfg, c, nil)
	go w.Start()
	defer w.Shutdown()

	time.Sleep(50 * time.Millisecond)

	// Trigger a claim that will get a 409
	w.HandleMessage(ws.WSMessage{Type: "ticket_available", TicketID: strPtr("uuid-5")})

	time.Sleep(200 * time.Millisecond)

	if claimAttempts != 1 {
		t.Errorf("expected 1 claim attempt, got %d", claimAttempts)
	}
}
