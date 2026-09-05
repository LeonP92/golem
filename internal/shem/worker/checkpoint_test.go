package worker_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/worker"
)

// TestPostCheckpointWithRetry_RetriesAndSucceeds verifies that the function
// retries on 5xx responses and eventually succeeds.
func TestPostCheckpointWithRetry_RetriesAndSucceeds(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "err", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "k")
	c.RetryInitial = 10 * time.Millisecond
	c.RetryMax = 50 * time.Millisecond

	if err := worker.PostCheckpointWithRetry(c, "ticket-uuid-1", "implement", "abc", 5); err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

// TestPostCheckpointWithRetry_ExhaustsRetries verifies an error is returned
// when all attempts are consumed.
func TestPostCheckpointWithRetry_ExhaustsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "err", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "k")
	c.RetryInitial = 1 * time.Millisecond
	c.RetryMax = 5 * time.Millisecond

	err := worker.PostCheckpointWithRetry(c, "ticket-uuid-1", "implement", "abc", 2)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

// TestPostCheckpointWithRetry_RestoresRetryAttempts verifies that the client's
// RetryAttempts field is restored to its original value after the call.
func TestPostCheckpointWithRetry_RestoresRetryAttempts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "k")
	origAttempts := c.RetryAttempts

	_ = worker.PostCheckpointWithRetry(c, "ticket-uuid-1", "plan", "sha1", 3)

	if c.RetryAttempts != origAttempts {
		t.Errorf("RetryAttempts not restored: got %d, want %d", c.RetryAttempts, origAttempts)
	}
}
