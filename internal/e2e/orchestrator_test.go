package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// openTestDB opens an in-memory SQLite database with a single connection so all
// goroutines share the same in-memory state (multiple open connections each get
// their own empty in-memory database).
func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("gdb.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	return gdb
}

// seedTestShem creates a Shem row with a bcrypt-hashed API key and returns
// both the row and the plain-text key.
func seedTestShem(t *testing.T, gdb *gorm.DB, name, plainKey string) db.Shem {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(plainKey), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	shem := db.Shem{Name: name, APIKeyHash: string(hash), Repos: `["https://github.com/org/repo"]`, Status: "online"}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatalf("create shem: %v", err)
	}
	return shem
}

// TestConcurrentClaim_ExactlyOneWinner fires 20 goroutines all trying to claim
// the same unassigned ticket. Exactly one must succeed; all others must fail.
func TestConcurrentClaim_ExactlyOneWinner(t *testing.T) {
	gdb := openTestDB(t)

	// Seed shems (one per goroutine so each has a valid ID).
	const n = 20
	shems := make([]db.Shem, n)
	for i := range shems {
		shems[i] = seedTestShem(t, gdb, fmt.Sprintf("shem-%d", i), fmt.Sprintf("key-%d", i))
	}

	ticket := db.Ticket{
		RepoRemote:  "https://github.com/org/repo",
		Branch:      "ticket/concurrent",
		Description: "concurrent claim test",
		Phase:       "unassigned",
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())

	wins := make([]bool, n)
	var wg sync.WaitGroup
	for i := range wins {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := h.ClaimTicket(ticket.ID, shems[idx].ID)
			wins[idx] = err == nil
		}(i)
	}
	wg.Wait()

	count := 0
	for _, w := range wins {
		if w {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 winner, got %d", count)
	}

	// Confirm DB state.
	var got db.Ticket
	gdb.First(&got, "id = ?", ticket.ID)
	if got.Phase != "claimed" {
		t.Errorf("expected phase=claimed, got %q", got.Phase)
	}
	if got.AssignedShem == nil {
		t.Error("expected assigned_shem to be set")
	}
}

// TestHeartbeatMonitor_RequeuesAndNewShemClaims simulates a dead Shem-A that
// held a ticket. The heartbeat monitor should mark it offline and requeue the
// ticket. Shem-B can then claim it.
func TestHeartbeatMonitor_RequeuesAndNewShemClaims(t *testing.T) {
	gdb := openTestDB(t)

	old := time.Now().Add(-3 * time.Minute)
	deadShem := db.Shem{
		Name:          "dead-shem",
		APIKeyHash:    "x",
		Repos:         `["https://github.com/org/repo"]`,
		Status:        "online",
		LastHeartbeat: &old,
	}
	if err := gdb.Create(&deadShem).Error; err != nil {
		t.Fatalf("create dead shem: %v", err)
	}

	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Branch:       "ticket/recovery",
		Description:  "recovery test",
		Phase:        "implement",
		AssignedShem: &deadShem.ID,
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hub := ws.NewHub()
	// checkInterval=50ms, timeout=60s (shem is 3min stale → already dead).
	ws.StartHeartbeatMonitor(ctx, gdb, hub, 50*time.Millisecond, 60*time.Second)
	time.Sleep(200 * time.Millisecond)

	// Ticket should now be unassigned.
	var got db.Ticket
	gdb.First(&got, "id = ?", ticket.ID)
	if got.Phase != "unassigned" {
		t.Fatalf("expected unassigned after shem death, got %q", got.Phase)
	}
	if got.AssignedShem != nil {
		t.Error("expected assigned_shem to be nil after requeue")
	}

	// Shem-B can now claim the recovered ticket.
	liveShem := seedTestShem(t, gdb, "live-shem", "livekey")
	h := api.NewHandlers(gdb, hub, sse.NewBroker())
	claim, err := h.ClaimTicket(ticket.ID, liveShem.ID)
	if err != nil {
		t.Fatalf("live shem could not claim recovered ticket: %v", err)
	}
	if claim.TicketID != ticket.ID {
		t.Errorf("expected ticket_id=%s, got %s", ticket.ID, claim.TicketID)
	}
	// No checkpoint was posted by the dead shem, so CheckpointPhase must be nil.
	if claim.CheckpointPhase != nil {
		t.Errorf("expected nil CheckpointPhase (fresh ticket), got %v", claim.CheckpointPhase)
	}
}

// TestLogForwarding_SSEDeliversInSequenceOrder posts three log entries via the
// HTTP endpoint and asserts the SSE broker delivers them in sequence-number
// order to a direct subscriber.
func TestLogForwarding_SSEDeliversInSequenceOrder(t *testing.T) {
	gdb := openTestDB(t)

	shem := seedTestShem(t, gdb, "log-shem", "logkey")
	ticket := db.Ticket{
		RepoRemote:   "https://github.com/org/repo",
		Branch:       "ticket/log-forward",
		Description:  "log forwarding test",
		Phase:        "implement",
		AssignedShem: &shem.ID,
	}
	if err := gdb.Create(&ticket).Error; err != nil {
		t.Fatalf("create ticket: %v", err)
	}

	broker := sse.NewBroker()
	h := api.NewHandlers(gdb, ws.NewHub(), broker)
	mux := http.NewServeMux()
	h.RegisterLogRoutes(mux)

	// Subscribe before posting so we don't miss any events.
	ch, cancel := broker.Subscribe(ticket.ID)
	defer cancel()

	messages := []string{"alpha", "beta", "gamma"}
	for _, msg := range messages {
		body, _ := json.Marshal(map[string]string{
			"entry_type": "STATUS",
			"from_role":  "developer",
			"message":    msg,
		})
		url := fmt.Sprintf("/api/tickets/%s/log", ticket.ID)
		req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer logkey")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST log %q: expected 200, got %d: %s", msg, w.Code, w.Body.String())
		}
	}

	// Drain the channel and verify sequence numbers are strictly ascending.
	received := make([]sse.LogEntryEvent, 0, len(messages))
	timeout := time.After(2 * time.Second)
	for len(received) < len(messages) {
		select {
		case evt := <-ch:
			received = append(received, evt)
		case <-timeout:
			t.Fatalf("timed out waiting for SSE events; received %d/%d", len(received), len(messages))
		}
	}

	for i, evt := range received {
		wantSeq := uint(i + 1)
		if evt.SequenceNum != wantSeq {
			t.Errorf("event[%d]: expected sequence_num=%d, got %d", i, wantSeq, evt.SequenceNum)
		}
		if evt.Message != messages[i] {
			t.Errorf("event[%d]: expected message=%q, got %q", i, messages[i], evt.Message)
		}
	}
}
