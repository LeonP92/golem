package worker

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// A ticket the orchestrator reaped while this shem was still running it must
// be taken back, and only that: a reaped ticket this shem is NOT running is
// left in the pool for a normal claim, which resumes it from its checkpoint.
//
// Seen live: a database stall delayed heartbeats past the reaper's timeout.
// Three tickets went back to unassigned while their agents kept running
// here; the poll loop skipped them as already running, and each run would
// finish into "ticket not owned by this shem" and start implementation over.
func TestReclaimReaped_TakesBackOnlyTicketsRunningHere(t *testing.T) {
	var mu sync.Mutex
	var reclaimed []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/tickets/reaped":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"ticket_id":"running-1"},{"ticket_id":"running-2"},{"ticket_id":"not-here"}]`))
		case strings.HasSuffix(r.URL.Path, "/reclaim"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/tickets/"), "/reclaim")
			mu.Lock()
			reclaimed = append(reclaimed, id)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	w := New(&config.Config{}, client.New(srv.URL, "k", "shem-a"), nil, nil)
	w.running["running-1"] = func() {}
	w.running["running-2"] = func() {}
	w.running["not-reaped"] = func() {}

	w.reclaimReaped()

	mu.Lock()
	defer mu.Unlock()
	sort.Strings(reclaimed)
	if strings.Join(reclaimed, ",") != "running-1,running-2" {
		t.Errorf("reclaimed %v, want [running-1 running-2]", reclaimed)
	}
}

// With nothing running there is nothing to take back, so the worker must not
// ask at all: this runs on every poll tick.
func TestReclaimReaped_IdleShemMakesNoRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(srv.Close)

	w := New(&config.Config{}, client.New(srv.URL, "k", "shem-a"), nil, nil)
	w.reclaimReaped()
}
