package client_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/models"
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
	var body struct {
		Backend struct {
			Name    string         `json:"name"`
			Catalog models.Catalog `json:"catalog"`
		} `json:"backend"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/shems/me" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"shem_id": 42}) //nolint:errcheck
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	catalog := models.Catalog{SupportsSelection: true, Models: []models.Model{{ID: "opus"}}}
	id, err := c.Register("node-a", []string{"https://github.com/org/repo"}, "claude-code", catalog)
	if err != nil {
		t.Fatal(err)
	}
	if id != 42 {
		t.Errorf("expected id 42, got %d", id)
	}
	if body.Backend.Name != "claude-code" {
		t.Errorf("reported backend = %q, want claude-code", body.Backend.Name)
	}
	if len(body.Backend.Catalog.Models) != 1 || body.Backend.Catalog.Models[0].ID != "opus" {
		t.Errorf("reported catalog = %+v, want one model opus", body.Backend.Catalog)
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

func TestClient_ClaimRevision_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/tickets/some-uuid/revise-claim" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		revisingPhase := "revising"
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.ClaimResponse{ //nolint:errcheck
			TicketID:        "some-uuid",
			CheckpointPhase: &revisingPhase,
		})
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	resp, err := c.ClaimRevision("some-uuid")
	if err != nil {
		t.Fatal(err)
	}
	if resp.CheckpointPhase == nil || *resp.CheckpointPhase != "revising" {
		t.Errorf("expected checkpoint_phase=revising, got %v", resp.CheckpointPhase)
	}
}

func TestClient_ClaimRevision_409(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "test-shem")
	c.RetryInitial = 10 * time.Millisecond
	_, err := c.ClaimRevision("some-uuid")
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

func TestPostBranchPushed(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "shem-a")
	if err := c.PostBranchPushed("t1", ""); err != nil {
		t.Fatalf("PostBranchPushed: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/tickets/t1/branch-pushed" {
		t.Errorf("got %s %s, want POST /api/tickets/t1/branch-pushed", gotMethod, gotPath)
	}
}

func TestPostBranchPushed_NotOwner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "shem-a")
	c.RetryInitial = 10 * time.Millisecond
	if err := c.PostBranchPushed("t1", ""); !errors.Is(err, client.ErrNotOwner) {
		t.Errorf("expected ErrNotOwner, got %v", err)
	}
}

// The orchestrator refuses a write to someone else's ticket with a plain-text
// 409. PostLog decoded that body as JSON and failed with
// "invalid character 'i' in literal true" — the "ti" of "ticket not owned" —
// which hid the real cause in the shem log.
func TestPostLog_NotOwner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ticket not owned by this shem", http.StatusConflict)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "shem-a")
	c.RetryInitial = 10 * time.Millisecond
	if _, err := c.PostLog("t1", client.LogPayload{EntryType: "STATUS", Message: "m"}); !errors.Is(err, client.ErrNotOwner) {
		t.Errorf("expected ErrNotOwner, got %v", err)
	}
}

func TestPostLog_OtherErrorNamesTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "shem-a")
	c.RetryInitial = 10 * time.Millisecond
	_, err := c.PostLog("t1", client.LogPayload{EntryType: "STATUS", Message: "m"})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("expected an error naming status 400, got %v", err)
	}
}

// PostCheckpoint ignored the status entirely, so a checkpoint the
// orchestrator refused was reported as saved. The checkpoint endpoint answers
// a non-owner with 404 ("not found or not owner"); 409 is accepted too, the
// convention the other ownership-scoped writes use.
func TestPostCheckpoint_NotOwner(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusConflict} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found or not owner", code)
		}))
		c := client.New(srv.URL, "key", "shem-a")
		c.RetryInitial = 10 * time.Millisecond
		if err := c.PostCheckpoint("t1", "plan", ""); !errors.Is(err, client.ErrNotOwner) {
			t.Errorf("status %d: expected ErrNotOwner, got %v", code, err)
		}
		srv.Close()
	}
}

func TestGetReaped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tickets/reaped" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ticket_id":"t1"},{"ticket_id":"t2"}]`))
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "shem-a")
	got, err := c.GetReaped()
	if err != nil {
		t.Fatalf("GetReaped: %v", err)
	}
	if len(got) != 2 || got[0] != "t1" || got[1] != "t2" {
		t.Errorf("got %v, want [t1 t2]", got)
	}
}

func TestReclaim(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.Path
		if strings.Contains(r.URL.Path, "/taken/") {
			http.Error(w, "ticket is not reclaimable", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := client.New(srv.URL, "key", "shem-a")
	c.RetryInitial = 10 * time.Millisecond
	if err := c.Reclaim("t1"); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}
	if gotPath != "POST /api/tickets/t1/reclaim" {
		t.Errorf("got %s", gotPath)
	}
	if err := c.Reclaim("taken"); !errors.Is(err, client.ErrNotAvailable) {
		t.Errorf("expected ErrNotAvailable, got %v", err)
	}
}
