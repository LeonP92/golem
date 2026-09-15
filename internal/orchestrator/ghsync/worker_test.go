package ghsync_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
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

// TestTriggerSyncReleasesSlotAfterCompletion pins fix-round-1 item 4: the
// centrepiece of the notify-channel correction is that whichever code path
// consumes a trigger (ingestLoop's `case repoID := <-w.notify`) releases that
// repo's slot afterwards, so a later TriggerSync for the same repo can queue
// again. Nothing in the original two brief tests would catch a regression
// that moved releaseSlot before ingestOne (reintroducing overlapping ingests)
// or dropped it on an error path — this test asserts release happens across
// three outcomes of the consumed ingest: success, an IngestRepo error, and a
// repo ID that doesn't exist at all (ingestOne's early-return path).
func TestTriggerSyncReleasesSlotAfterCompletion(t *testing.T) {
	tests := []struct {
		name string
		seed func(t *testing.T, gdb *gorm.DB, f *github.Fake) uint // returns the repo ID to trigger
	}{
		{
			name: "successful ingest",
			seed: func(t *testing.T, gdb *gorm.DB, f *github.Fake) uint {
				repo := newRepo(t, gdb)
				f.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
					UpdatedAt: time.Now(), Labels: []string{"golem"}})
				return repo.ID
			},
		},
		{
			name: "IngestRepo returns an error",
			seed: func(t *testing.T, gdb *gorm.DB, f *github.Fake) uint {
				repo := newRepo(t, gdb)
				f.SetFailNext(errors.New("boom"))
				return repo.ID
			},
		},
		{
			name: "unknown repo ID (ingestOne's early-return path)",
			seed: func(t *testing.T, gdb *gorm.DB, f *github.Fake) uint {
				return 999999
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gdb, _ := db.Open(":memory:")
			f := github.NewFake()
			repoID := tt.seed(t, gdb, f)

			// Both tickers slow: only the manual trigger drives ingestLoop.
			w := ghsync.NewWorker(ghsync.NewSyncer(gdb, f), time.Hour, time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.Start(ctx)
			defer w.Stop()

			if !w.TriggerSync(repoID) {
				t.Fatal("first TriggerSync = false, want true")
			}
			// While the first trigger is queued or being processed, a second
			// TriggerSync for the same repo must fail (the slot is still
			// held). It must eventually succeed once ingestLoop's consumer
			// releases the slot — that transition is the release itself.
			waitFor(t, time.Second, func() bool {
				return w.TriggerSync(repoID)
			}, "slot released after ingestOne completed so a second TriggerSync can queue")
		})
	}
}

// TestStopIsIdempotent covers fix-round-1 item 4's second ask: Stop must be
// safe to call before Start ever runs, and safe to call more than once
// (including concurrently), in both cases returning rather than deadlocking.
func TestStopIsIdempotent(t *testing.T) {
	t.Run("stop before start returns immediately", func(t *testing.T) {
		gdb, _ := db.Open(":memory:")
		w := ghsync.NewWorker(ghsync.NewSyncer(gdb, github.NewFake()), time.Hour, time.Hour)

		done := make(chan struct{})
		go func() {
			w.Stop()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Stop before Start did not return — done.Wait() blocked with nothing ever added")
		}
	})

	t.Run("concurrent Stop calls both return", func(t *testing.T) {
		gdb, _ := db.Open(":memory:")
		w := ghsync.NewWorker(ghsync.NewSyncer(gdb, github.NewFake()), time.Hour, time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		w.Start(ctx)

		var wg sync.WaitGroup
		wg.Add(3)
		for i := 0; i < 3; i++ {
			go func() {
				defer wg.Done()
				w.Stop()
			}()
		}
		stopped := make(chan struct{})
		go func() {
			wg.Wait()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("concurrent Stop calls did not all return")
		}
	})
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
