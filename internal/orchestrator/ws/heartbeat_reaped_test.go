package ws_test

import (
	"context"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
)

// A reaped ticket must remember which shem held it and what phase it was in.
//
// The reaper decides a shem is dead from a stale heartbeat alone, and a live
// shem can miss heartbeats — seen live when the orchestrator's own database
// stalled for 7–35 seconds per write. The shem kept its agents running, but
// every ticket in flight went back to unassigned with nothing recording
// where it came from, so the shem could not take it back: each run finished
// into "ticket not owned by this shem" and started implementation over.
func TestHeartbeatMonitor_RecordsWhereAReapedTicketCameFrom(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("gdb.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	old := time.Now().Add(-2 * time.Minute)
	shem := db.Shem{Name: "stalled", APIKeyHash: "x", Repos: "[]", Status: "online", LastHeartbeat: &old}
	gdb.Create(&shem)
	ticket := db.Ticket{RepoRemote: "https://github.com/org/r", Branch: "ticket/1",
		Description: "test", Phase: "implement", AssignedShem: &shem.ID}
	gdb.Create(&ticket)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws.StartHeartbeatMonitor(ctx, gdb, ws.NewHub(), 50*time.Millisecond, 90*time.Second)
	time.Sleep(200 * time.Millisecond)

	var got db.Ticket
	gdb.First(&got, "id = ?", ticket.ID)
	if got.Phase != "unassigned" {
		t.Fatalf("expected unassigned, got %q", got.Phase)
	}
	if got.ReapedFromShem == nil || *got.ReapedFromShem != shem.ID {
		t.Errorf("ReapedFromShem = %v, want %d", got.ReapedFromShem, shem.ID)
	}
	if got.ReapedFromPhase != "implement" {
		t.Errorf("ReapedFromPhase = %q, want implement", got.ReapedFromPhase)
	}
}
