package worker_test

import (
	"context"
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

// recordingExecutor captures the claim RunTicket was handed instead of running
// anything.
type recordingExecutor struct {
	claims chan *client.ClaimResponse
}

func (e *recordingExecutor) RunTicket(_ context.Context, _ *config.Config, _ *client.Client, claim *client.ClaimResponse) error {
	e.claims <- claim
	return nil
}

// TestWorker_ResumesTicketWithNoCheckpoint covers the startup resume of a
// ticket that has no checkpoint — a shem that died during its first phase,
// before one was written.
//
// /api/tickets/resumable used to require a checkpoint, so this claim shape
// never reached tryResumeTicket and its unconditional *claim.CheckpointPhase
// was safe. Widening the predicate to un-strand those tickets makes nil the
// normal case for precisely the tickets that were stuck, and a nil deref here
// is not a failed resume — it is a panic in a goroutine, which takes the whole
// shem process down on startup and takes every other running ticket with it.
func TestWorker_ResumesTicketWithNoCheckpoint(t *testing.T) {
	const ticketID = "uuid-no-checkpoint"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/tickets/resumable"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode([]client.ClaimResponse{{ //nolint:errcheck
				TicketID:   ticketID,
				Branch:     "ticket/no-checkpoint",
				RepoRemote: "r",
				// No CheckpointPhase: the shem died mid-brainstorm.
			}})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/register"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1}) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	exec := &recordingExecutor{claims: make(chan *client.ClaimResponse, 1)}
	cfg := &config.Config{
		Orchestrator: srv.URL,
		APIKey:       "k",
		Name:         "n",
		Repos: []config.RepoConfig{
			{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"},
		},
	}
	w := worker.New(cfg, client.New(srv.URL, "k", "test-shem"), exec)
	go w.Start()
	defer w.Shutdown()

	select {
	case got := <-exec.claims:
		if got.TicketID != ticketID {
			t.Errorf("resumed ticket %q, want %q", got.TicketID, ticketID)
		}
		// RunTicket branches on this: nil means "start the phase over",
		// non-nil means "skip everything up to here".
		if got.CheckpointPhase != nil {
			t.Errorf("claim reached the executor with checkpoint %q; it must stay nil "+
				"so RunTicket restarts the phase instead of skipping it", *got.CheckpointPhase)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: a resumable ticket with no checkpoint was never run")
	}
}
