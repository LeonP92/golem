package worker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/shem/worker"
)

// reviseServer answers as an orchestrator holding one ticket assigned to
// this shem and already in revising, with no websocket push ever sent.
func reviseServer(t *testing.T, claimed chan<- string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/revise-claim"):
			parts := strings.Split(r.URL.Path, "/")
			var id string
			for i, p := range parts {
				if p == "tickets" && i+1 < len(parts) {
					id = parts[i+1]
				}
			}
			select {
			case claimed <- id:
			default:
			}
			revising := "revising"
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(client.ClaimResponse{ //nolint:errcheck
				TicketID: id, Branch: "ticket/x-abc12345", BaseBranch: "main",
				RepoRemote: "r", CheckpointPhase: &revising,
			})
		case strings.HasSuffix(r.URL.Path, "/api/tickets/assigned"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]string{ //nolint:errcheck
				{"ticket_id": "stranded-1", "phase": "revising", "repo_remote": "r"},
			})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/register"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A ticket owed a revision must be picked up without a websocket push.
//
// The push is the only thing that started a revision, and it is transient:
// the orchestrator sends ticket_revise once, and anything that misses it —
// the shem restarting, a dropped connection, the orchestrator restarting
// before the shem reconnects — stranded the ticket for good. Nothing
// recovered it, because the poll loop asks only for AVAILABLE tickets and
// resumableTickets deliberately excludes revising.
//
// Seen live: three tickets the pull-request monitor moved to revising were
// reported by the shem on startup as "revising — waiting (no action
// needed)" and sat there. The push had been sent while the shem was
// restarting.
//
// So the phase itself, not the message, is the instruction. The push only
// makes it fast.
func TestWorker_PicksUpRevisingTicketsWithoutAPush(t *testing.T) {
	claimed := make(chan string, 1)
	srv := reviseServer(t, claimed)

	cfg := &config.Config{
		Orchestrator: srv.URL, APIKey: "k", Name: "n",
		Repos: []config.RepoConfig{{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"}},
	}
	w := worker.New(cfg, client.New(srv.URL, "k", "test-shem"), &recordingExecutor{
		claims: make(chan *client.ClaimResponse, 4),
	})
	go w.Start()
	defer w.Shutdown()

	select {
	case id := <-claimed:
		if id != "stranded-1" {
			t.Errorf("claimed %q, want stranded-1", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a ticket assigned to this shem and sitting in revising was never claimed; " +
			"it is stranded until someone notices by hand")
	}
}
