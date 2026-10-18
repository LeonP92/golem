package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/forge"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

type movingForge struct{ merges int }

func (*movingForge) HeadSHA(context.Context, string, string) (string, error) { return "head", nil }
func (f *movingForge) MergeIntoTarget(context.Context, forge.Merge) (forge.Result, error) {
	f.merges++
	return forge.Result{Status: forge.Moved}, nil
}

func TestIntegrationMovedHeadWaitsForNextReconciliation(t *testing.T) {
	var parked bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/integrate-check"):
			_ = json.NewEncoder(w).Encode(client.IntegrationGate{Outcome: "granted", TargetRef: "epic/e", LeaseID: "lease", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339)})
		case strings.HasSuffix(r.URL.Path, "/integrate-result"):
			var result client.IntegrationResult
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			parked = result.Status == "error"
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	f := &movingForge{}
	e := &GolemExecutor{Forge: f}
	e.IntegrateAssigned(context.Background(), &config.Config{}, client.New(srv.URL, "key", "shem"),
		&client.ClaimResponse{TicketID: "ticket", RepoRemote: "remote", Epic: &client.EpicClaim{EpicID: "e", NodeKey: "n"}},
		t.TempDir(), "ticket/branch")
	if f.merges != 1 || parked {
		t.Fatalf("moving head must wait for polling: %d merges, parked=%v", f.merges, parked)
	}
}

// Exercise the actual poll loop, so recovery cannot silently regress to
// startup-only reconciliation again.
type pollIntegrationExecutor struct {
	attempts atomic.Int32
	retried  chan struct{}
}

func (*pollIntegrationExecutor) RunTicket(context.Context, *config.Config, *client.Client, *client.ClaimResponse) error {
	return nil
}
func (e *pollIntegrationExecutor) IntegrateAssigned(context.Context, *config.Config, *client.Client, *client.ClaimResponse, string, string) {
	if e.attempts.Add(1) == 2 {
		close(e.retried)
	}
}
func TestWorkerPollReconcilesFinishedIntegrations(t *testing.T) {
	assigned := []client.AssignedTicket{{TicketID: "t", Phase: "ready-for-review", RepoRemote: "r", EpicID: "e", EpicNodeKey: "A", Branch: "ticket/t"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tickets/assigned" {
			_ = json.NewEncoder(w).Encode(assigned)
			return
		}
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	defer srv.Close()
	ex := &pollIntegrationExecutor{retried: make(chan struct{})}
	w := New(&config.Config{Repos: []config.RepoConfig{{Path: t.TempDir(), NormalizedRemote: "r"}}}, client.New(srv.URL, "k", "s"), ex, nil)
	w.integrateAssigned(assigned)
	done := make(chan struct{})
	go func() { w.pollLoop(); close(done) }()
	defer func() { close(w.stop); <-done }()
	select {
	case <-ex.retried:
	case <-time.After(15 * time.Second):
		t.Fatal("poll loop never retried finished child")
	}
}
