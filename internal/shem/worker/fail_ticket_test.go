package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/shem/worker"
)

var errShemSetup = errors.New("golem ticket new: exit status 1")

// failingExecutor stands in for a ticket that cannot start — the shape the
// operator hit, where `golem ticket new` failed during "Initializing
// repository…".
type failingExecutor struct{ err error }

func (f failingExecutor) RunTicket(_ context.Context, _ *config.Config, _ *client.Client, _ *client.ClaimResponse) error {
	return f.err
}

// A ticket parked in needs-attention has to say why, on the ticket.
//
// Before this the reason went to the shem's stdout and nowhere else, so the
// dashboard showed a red badge above an activity log whose last line was the
// step that had been announced before the failure. The phase claimed there
// was a problem and the log offered no evidence of one, which is exactly the
// combination that reads as a bug in Golem rather than a failure in the work.
func TestFailedTicketRecordsWhyBeforeNeedsAttention(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	var phases []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/log"):
			var body struct {
				EntryType string `json:"entry_type"`
				Message   string `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			logged = append(logged, body.EntryType+": "+body.Message)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1}`))
		case strings.HasSuffix(r.URL.Path, "/phase"):
			var body struct {
				Phase string `json:"phase"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			phases = append(phases, body.Phase)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/claim"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ticket_id":"t-1","branch":"ticket/t-1","description":"d","repo_remote":"r"}`))
		case strings.Contains(r.URL.Path, "/register"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]uint{"shem_id": 1})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{
		Orchestrator: srv.URL, APIKey: "k", Name: "n",
		Repos: []config.RepoConfig{{Path: t.TempDir(), Remote: "r", NormalizedRemote: "r"}},
	}
	c := client.New(srv.URL, "k", "test-shem")
	w := worker.New(cfg, c, failingExecutor{err: errShemSetup})
	go w.Start()
	defer w.Shutdown()

	time.Sleep(50 * time.Millisecond)
	w.HandleMessage(ws.WSMessage{Type: "ticket_available", TicketID: strPtr("t-1")})

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		gotPhase := len(phases) > 0
		mu.Unlock()
		if gotPhase {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout: the ticket was never parked")
		case <-time.After(10 * time.Millisecond):
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(phases) == 0 || phases[len(phases)-1] != "needs-attention" {
		t.Fatalf("phases = %v, want the ticket parked in needs-attention", phases)
	}
	var found string
	for _, e := range logged {
		if strings.Contains(e, errShemSetup.Error()) {
			found = e
		}
	}
	if found == "" {
		t.Fatalf("the failure was never recorded on the ticket; entries = %v", logged)
	}
	if !strings.HasPrefix(found, "BLOCKER:") {
		t.Errorf("recorded as %q; a parked ticket's reason should be a BLOCKER", found)
	}
}
