package ws_test

import (
	"context"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

func TestHeartbeatMonitor_RequeuesDeadShem(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	// SQLite :memory: databases are per-connection. Limit the pool to one
	// connection so all operations share the same in-memory database.
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("gdb.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	old := time.Now().Add(-2 * time.Minute)
	shem := db.Shem{Name: "dead", APIKeyHash: "x", Repos: "[]", Status: "online", LastHeartbeat: &old}
	gdb.Create(&shem)
	ticket := db.Ticket{RepoRemote: "https://github.com/org/r", Branch: "ticket/1",
		Description: "test", Phase: "implement", AssignedShem: &shem.ID}
	gdb.Create(&ticket)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	hub := ws.NewHub()
	ws.StartHeartbeatMonitor(ctx, gdb, hub, 50*time.Millisecond, 90*time.Second)
	time.Sleep(200 * time.Millisecond)

	var got db.Ticket
	gdb.First(&got, "id = ?", ticket.ID)
	if got.Phase != "unassigned" {
		t.Errorf("expected unassigned, got %q", got.Phase)
	}
	if got.AssignedShem != nil {
		t.Error("expected nil assigned_shem")
	}
}
