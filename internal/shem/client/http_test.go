package client_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
)

func TestClient_RetriesOn5xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	c.RetryMax = 5 * time.Second
	err := c.PostPhase("ticket-1", "brainstorm")
	if err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestClient_AuthorizationHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "myapikey", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	_ = c.PostPhase("ticket-1", "brainstorm")
	if gotAuth != "Bearer myapikey" {
		t.Errorf("expected 'Bearer myapikey', got %q", gotAuth)
	}
}

func TestClient_ExhaustsRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 1 * time.Millisecond
	c.RetryMax = 5 * time.Millisecond
	c.RetryAttempts = 3
	err := c.PostPhase("ticket-1", "brainstorm")
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestClient_Register(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/shems/register" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"shem_id": 42}) //nolint:errcheck
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	id, err := c.Register("node-a", []string{"https://github.com/org/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Errorf("expected id 42, got %d", id)
	}
}

func TestClient_ClaimTicket_409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	_, err := c.ClaimTicket("some-uuid")
	if err != client.ErrNotAvailable {
		t.Errorf("expected ErrNotAvailable, got %v", err)
	}
}

func TestClient_GetAvailable(t *testing.T) {
	const wantID = "abc123-uuid"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tickets/available" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		type ticket struct {
			ID string `json:"id"`
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]ticket{{ID: wantID}}) //nolint:errcheck
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	id, err := c.GetAvailable("https://github.com/org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if id == nil {
		t.Fatal("expected a ticket id, got nil")
	}
	if *id != wantID {
		t.Errorf("expected id %q, got %q", wantID, *id)
	}
}

func TestClient_GetAvailable_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]struct{}{}) //nolint:errcheck
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	id, err := c.GetAvailable("https://github.com/org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if id != nil {
		t.Errorf("expected nil, got %q", *id)
	}
}
