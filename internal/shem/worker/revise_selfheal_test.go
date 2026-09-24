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

// A ticket restored to an active phase must be picked up without a restart.
//
// Review finding 4: actionResume sets the phase back and returns 204, but
// nothing dispatches. The poll loop handles only reviseAssigned (revising)
// and GetAvailable (unassigned); GetResumable is called once in Start(),
// before the loop begins. So "Start again" on a ticket stopped during
// brainstorm, plan, implement or review reported success and then sat
// there until the shem process happened to restart.
//
// Fixed the same way the revising case was, and for the same reason: the
// phase is the instruction. Polling resumable makes any route back into an
// active phase self-healing, including ones nobody has thought of yet,
// rather than requiring each to remember to send a message.
func TestWorker_PicksUpAResumedTicketWithoutRestarting(t *testing.T) {
	// GetResumable answers with the ticket only from the second call on, so
	// a pass that happens during Start() cannot be what satisfies this
	// test — it has to be the poll loop.
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/tickets/resumable"):
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls < 2 {
				json.NewEncoder(w).Encode([]client.ClaimResponse{}) //nolint:errcheck
				return
			}
			phase := "implement"
			json.NewEncoder(w).Encode([]client.ClaimResponse{{ //nolint:errcheck
				TicketID: "resumed-1", Branch: "ticket/x-abc12345", BaseBranch: "main",
				RepoRemote: "r", CheckpointPhase: &phase,
			}})
		case strings.HasSuffix(r.URL.Path, "/api/tickets/assigned"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]map[string]string{}) //nolint:errcheck
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/register"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	exec := &recordingExecutor{claims: make(chan *client.ClaimResponse, 4)}
	cfg := &config.Config{
		Orchestrator: srv.URL, APIKey: "k", Name: "n",
		Repos: []config.RepoConfig{{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"}},
	}
	w := worker.New(cfg, client.New(srv.URL, "k", "test-shem"), exec)
	go w.Start()
	defer w.Shutdown()

	select {
	case got := <-exec.claims:
		if got.TicketID != "resumed-1" {
			t.Errorf("ran %q, want resumed-1", got.TicketID)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a ticket restored to an active phase was never picked up; " +
			"\"Start again\" reports success and then nothing happens until a restart")
	}
}
