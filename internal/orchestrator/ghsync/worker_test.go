package ghsync_test

import (
	"context"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

func TestWorkerIngestsOnItsTicker(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	newRepo(t, gdb)
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	w := ghsync.NewWorker(ghsync.NewSyncer(gdb, f), 10*time.Millisecond, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	waitFor(t, time.Second, func() bool {
		var n int64
		gdb.Model(&db.Ticket{}).Count(&n)
		return n == 1
	}, "ticket created by the ingest ticker")
}

func TestWorkerDrainsOnItsOwnTicker(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	seedLinkedTicket(t, gdb, "plan")
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, State: "open", Labels: []string{"golem"}})
	if err := ghsync.Enqueue(gdb, db.GitHubOutbox{
		TicketID: "t1", Kind: ghsync.KindComment, Payload: `{"body":"hi"}`,
		IdempotencyKey: ghsync.CommentKey("t1", "spec"),
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Ingest ticker set to an hour: only the drain ticker can do this work.
	w := ghsync.NewWorker(ghsync.NewSyncer(gdb, f), time.Hour, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	waitFor(t, time.Second, func() bool {
		return len(f.CommentsFor(7)) == 1
	}, "comment posted by the drain ticker")
}

func TestTriggerSyncRunsIngestImmediately(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	repo := newRepo(t, gdb)
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	// Both tickers slow: only the manual trigger can produce the ticket.
	w := ghsync.NewWorker(ghsync.NewSyncer(gdb, f), time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	if !w.TriggerSync(repo.ID) {
		t.Fatal("TriggerSync returned false on an idle worker")
	}
	waitFor(t, time.Second, func() bool {
		var n int64
		gdb.Model(&db.Ticket{}).Count(&n)
		return n == 1
	}, "ticket created by the manual trigger")
}

func TestTriggerSyncCoalesces(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	repo := newRepo(t, gdb)
	w := ghsync.NewWorker(ghsync.NewSyncer(gdb, github.NewFake()), time.Hour, time.Hour)

	// Without Start, nothing consumes the channel: the first send buffers and
	// the second must be dropped rather than block.
	if !w.TriggerSync(repo.ID) {
		t.Error("first TriggerSync = false, want true")
	}
	if w.TriggerSync(repo.ID) {
		t.Error("second TriggerSync = true, want false — a sync is already queued")
	}
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
