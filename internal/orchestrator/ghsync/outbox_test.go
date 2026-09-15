package ghsync_test

import (
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

func TestEnqueueIsIdempotent(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	row := func() db.GitHubOutbox {
		return db.GitHubOutbox{
			TicketID:       "t1",
			Kind:           ghsync.KindComment,
			Payload:        `{"body":"spec ready"}`,
			IdempotencyKey: ghsync.CommentKey("t1", "spec"),
		}
	}

	first := row()
	if err := ghsync.Enqueue(gdb, first); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	second := row()
	if err := ghsync.Enqueue(gdb, second); err != nil {
		t.Fatalf("duplicate Enqueue should be a no-op, got: %v", err)
	}

	var n int64
	gdb.Model(&db.GitHubOutbox{}).Where("ticket_id = ?", "t1").Count(&n)
	if n != 1 {
		t.Errorf("row count = %d, want 1", n)
	}
}

func TestEnqueueSetsNextAttemptImmediately(t *testing.T) {
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindLabel, Payload: `{"phase":"plan"}`,
		IdempotencyKey: ghsync.LabelKey("t1", "plan"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	var got db.GitHubOutbox
	if err := gdb.First(&got, "ticket_id = ?", "t1").Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if got.NextAttempt.IsZero() {
		t.Error("NextAttempt is zero, want a time so the first drain picks it up")
	}
	if got.DoneAt != nil {
		t.Error("DoneAt set on a fresh row, want nil")
	}
}

func TestKeysAreDistinct(t *testing.T) {
	keys := map[string]bool{
		ghsync.CommentKey("t1", "spec"): true,
		ghsync.CommentKey("t1", "plan"): true,
		ghsync.LabelKey("t1", "plan"):   true,
		ghsync.CloseKey("t1"):           true,
		ghsync.PRKey("t1"):              true,
	}
	if len(keys) != 5 {
		t.Errorf("got %d distinct keys, want 5 — keys collide", len(keys))
	}
}
