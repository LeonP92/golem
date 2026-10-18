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

// testAgentFor builds an Agent from cfg; an absent backend block resolves to
// the default adapter.
func testAgentFor(t *testing.T, cfg *config.Config) *worker.Agent {
	t.Helper()
	agent, cleanup, err := worker.NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	t.Cleanup(cleanup)
	return agent
}

// startTestWorker serves handler, points a worker at it and starts the
// worker, with a nil executor so no golem actually runs. The worker shuts
// down before the server closes: Shutdown deregisters from the server, and
// the client retries a connection refusal for a long time.
func startTestWorker(t *testing.T, handler http.HandlerFunc) *worker.Worker {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		Orchestrator: srv.URL,
		APIKey:       "k",
		Name:         "n",
		Repos:        []config.RepoConfig{{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"}},
	}
	c := client.New(srv.URL, "k", "test-shem")
	w := worker.New(cfg, c, nil, testAgentFor(t, cfg))
	go w.Start()
	t.Cleanup(w.Shutdown)
	return w
}

func TestWorker_ClaimsOnPush(t *testing.T) {
	claimed := make(chan string, 1)

	w := startTestWorker(t, func(w http.ResponseWriter, r *http.Request) {
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
	})

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

	w := startTestWorker(t, func(w http.ResponseWriter, r *http.Request) {
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
	})

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
	testClaim409NotRetried(t, "/revise-claim", "ticket_revise")
}

func TestWorker_IgnoresNonAvailableMessages(t *testing.T) {
	claimed := make(chan string, 1)

	w := startTestWorker(t, func(w http.ResponseWriter, r *http.Request) {
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
	})

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
	testClaim409NotRetried(t, "/claim", "ticket_available")
}

// testClaim409NotRetried answers 409 to requests for pathSuffix and checks
// the worker makes exactly one attempt: a retried 409 would double-claim a
// ticket another shem already took. attempts is a buffered channel read by
// the test rather than a bare counter, because the handler runs on the
// server's goroutine and the assertion on the test's — a data race when the
// two are joined only by a sleep.
func testClaim409NotRetried(t *testing.T, pathSuffix, msgType string) {
	t.Helper()
	attempts := make(chan struct{}, 8)

	w := startTestWorker(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, pathSuffix) {
			attempts <- struct{}{}
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		if strings.Contains(r.URL.Path, "/register") {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	time.Sleep(50 * time.Millisecond)

	w.HandleMessage(ws.WSMessage{Type: msgType, TicketID: strPtr("uuid-5")})

	select {
	case <-attempts:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: no claim attempt was made")
	}
	// A second attempt would mean the 409 was retried rather than
	// treated as terminal, which is the whole point of the test.
	select {
	case <-attempts:
		t.Error("a second claim attempt was made; a 409 must not be retried")
	case <-time.After(200 * time.Millisecond):
	}
}
