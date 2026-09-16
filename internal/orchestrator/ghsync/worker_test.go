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
// again. It asserts release happens across three outcomes of the consumed
// ingest: success, an IngestRepo error, and a repo ID that doesn't exist at
// all (ingestOne's early-return path).
//
// It does NOT by itself catch releaseSlot being reordered before ingestOne —
// that regression only shows up while the ingest is still in flight, which
// this test's polling can miss (see fix-round-2 item 1's finding).
// TestTriggerSyncHoldsSlotWhileIngestInFlight below is the test that pins the
// ordering; this one is about the outcome (release eventually happens) and,
// per fix-round-2 item 2, that the "error" case's failure path is actually
// exercised rather than silently degrading to a success.
func TestTriggerSyncReleasesSlotAfterCompletion(t *testing.T) {
	tests := []struct {
		name      string
		seed      func(t *testing.T, gdb *gorm.DB, f *github.Fake) uint // returns the repo ID to trigger
		wantError bool
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
			wantError: true,
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

			// Both tickers slow: only the manual trigger drives ingestLoop.
			w := ghsync.NewWorker(ghsync.NewSyncer(gdb, f), time.Hour, time.Hour)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.Start(ctx)
			defer w.Stop()

			// ingestLoop runs one ingestAll pass unconditionally at startup,
			// before it ever reaches its select loop — and right now the DB
			// has no repos at all, so that pass is a fast no-op. Synchronize
			// on a probe trigger's full round trip (claim -> notify ->
			// ingestOne -> release) before seeding: a single goroutine
			// cannot reach the select loop before the statement preceding it
			// (the startup pass) has returned, so once the probe's slot is
			// released we know that pass is over. Without this, seeding the
			// real repo — and, for the error case, arming FailNext — before
			// the startup pass has run would let that pass consume the
			// armed failure itself and test nothing, which is exactly what
			// fix-round-2 item 2 found (proven via the fake's call log
			// showing the failure consumed before the manual trigger ever
			// fired).
			const probeRepoID = 999998
			if !w.TriggerSync(probeRepoID) {
				t.Fatal("probe TriggerSync returned false")
			}
			waitFor(t, time.Second, func() bool {
				return w.TriggerSync(probeRepoID)
			}, "probe's slot released — ingestLoop's select loop is now running, past its startup pass")

			repoID := tt.seed(t, gdb, f)

			if !w.TriggerSync(repoID) {
				t.Fatal("first TriggerSync = false, want true")
			}
			if tt.wantError {
				// Checked before the release-verification poll below. That
				// poll itself queues one more (by-then-successful, since
				// FailNext is consumed on first use) pass, which would
				// otherwise race this read by clearing LastError back to ""
				// again before we ever got to look at it.
				waitFor(t, time.Second, func() bool {
					var repo db.GitHubRepo
					if err := gdb.First(&repo, repoID).Error; err != nil {
						return false
					}
					return repo.LastError != ""
				}, "IngestRepo's armed failure was recorded on the repo — proves the error path actually ran rather than silently succeeding")
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

// blockingIngestClient wraps a Fake but blocks every ListIssuesSince call
// until release is closed, letting a test observe — and hold open for as
// long as it likes — an ingest that is "in flight". started is closed the
// first time ListIssuesSince is entered, so a test can wait for that instant
// without a fixed sleep.
type blockingIngestClient struct {
	*github.Fake
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingIngestClient(f *github.Fake) *blockingIngestClient {
	return &blockingIngestClient{Fake: f, started: make(chan struct{}), release: make(chan struct{})}
}

func (c *blockingIngestClient) ListIssuesSince(ctx context.Context, owner, repo, label string, since time.Time, etag string) (github.IssuePage, error) {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return c.Fake.ListIssuesSince(ctx, owner, repo, label, since, etag)
}

// TestTriggerSyncHoldsSlotWhileIngestInFlight is fix-round-2 item 1's test:
// TestTriggerSyncReleasesSlotAfterCompletion only ever observes release
// *eventually*, so a regression that moved releaseSlot to BEFORE ingestOne —
// reintroducing overlapping ingests and interleaved cursor writes, the exact
// bug the notify-channel correction exists to prevent — still passes that
// test; it just makes the slot release sooner, which the polling happily
// accepts. This test uses a Client whose ListIssuesSince blocks on demand so
// it can assert the slot is still HELD while the ingest is genuinely in
// flight, not just eventually released.
func TestTriggerSyncHoldsSlotWhileIngestInFlight(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	blocking := newBlockingIngestClient(github.NewFake())
	w := ghsync.NewWorker(ghsync.NewSyncer(gdb, blocking), time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	// Synchronize past the worker's one-time startup ingest pass using an
	// unrelated repo ID as a probe (see the identical technique, and the
	// fuller explanation, in TestTriggerSyncReleasesSlotAfterCompletion). The
	// DB has no repos at all yet, so this pass never touches the blocking
	// client.
	const probeRepoID = 999997
	if !w.TriggerSync(probeRepoID) {
		t.Fatal("probe TriggerSync returned false")
	}
	waitFor(t, time.Second, func() bool {
		return w.TriggerSync(probeRepoID)
	}, "probe's slot released — confirms ingestLoop is past its startup pass")

	// Only now create the real repo: no further automatic ingestAll pass can
	// run before the (1-hour) ticker fires, so from here on the only way
	// IngestRepo can be called for it is through our manual trigger below.
	repo := newRepo(t, gdb)
	blocking.AddIssue(github.Issue{Number: 7, Title: "t", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	if !w.TriggerSync(repo.ID) {
		t.Fatal("TriggerSync on the real repo returned false")
	}
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("ingest never started — IngestRepo (ListIssuesSince) was not called")
	}

	// The ingest is now blocked inside ListIssuesSince, with ingestOne not
	// yet returned. If releaseSlot ran before ingestOne (the regression this
	// test exists to catch), the slot would already be free here and this
	// would wrongly report true.
	if w.TriggerSync(repo.ID) {
		t.Error("TriggerSync returned true while the repo's ingest is still in flight — releaseSlot ran too early (before ingestOne returned)")
	}

	close(blocking.release)

	waitFor(t, time.Second, func() bool {
		return w.TriggerSync(repo.ID)
	}, "slot released once the blocked ingest completed")
}

// TestTriggerSyncOnSaturatedNotifyDoesNotBlock is the committed regression
// test for fix-round-1's CRITICAL 1, whose only previous coverage was a
// scratch repro file deleted after manual verification. Without the fix (an
// unguarded `w.notify <- repoID` after a successful slot claim), a 65th
// distinct repo's TriggerSync call blocks forever once notify's bounded
// buffer (64 — see notifyBuffer in worker.go) is full and nothing is
// draining it. A watchdog goroutine bounds the wait so a regression fails
// fast instead of hanging the suite.
func TestTriggerSyncOnSaturatedNotifyDoesNotBlock(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	w := ghsync.NewWorker(ghsync.NewSyncer(gdb, github.NewFake()), time.Hour, time.Hour)
	// No Start(): nothing ever drains notify, so its buffer fills and stays
	// full — the exact condition that exposed the hang.
	for i := uint(1); i <= 64; i++ {
		if !w.TriggerSync(i) {
			t.Fatalf("TriggerSync(%d) = false, want true (buffer not yet full)", i)
		}
	}

	result := make(chan bool, 1)
	go func() {
		result <- w.TriggerSync(65)
	}()
	select {
	case got := <-result:
		if got {
			t.Error("TriggerSync(65) = true, want false — nothing has ever claimed repo 65's slot and nothing drains notify, so a non-blocking send had no room to succeed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("TriggerSync(65) blocked for 2s on a saturated notify channel — the CRITICAL 1 regression is back")
	}
}

// TestTriggerSyncReturnsFalseAfterContextCancellation is fix-round-2 item 5's
// test: the stop guard added in fix-round-1 only checked w.stop, but both
// loops also exit on ctx.Done(). If the ctx passed to Start is cancelled
// without Stop() ever being called — exactly the pattern a Task 9 handler
// test using `ctx, cancel := ...; defer cancel()` would produce — w.stop is
// never closed, so a naive guard would keep letting TriggerSync claim slots
// and enqueue to a notify nobody will ever drain again, permanently
// stranding every repo it touches.
//
// This deliberately does NOT call TriggerSync in a loop while waiting for
// the exit to take effect. Doing so was tried and is actively misleading:
// each successful claim enqueues onto notify, and since Go's select chooses
// pseudo-randomly among simultaneously ready cases, a steady stream of
// notify sends can make ingestLoop's select keep picking the notify case
// over an already-ready ctx.Done() far longer than expected — a false
// failure caused by the test's own polling, not by a missing guard. Instead:
// cancel, wait in a single quiet window with nothing sent (so the only ready
// case left for ingestLoop to see is ctx.Done(), guaranteeing it exits well
// within the wait), then make exactly one, unambiguous check.
func TestTriggerSyncReturnsFalseAfterContextCancellation(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	w := ghsync.NewWorker(ghsync.NewSyncer(gdb, github.NewFake()), time.Hour, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	cancel() // no Stop() call.

	time.Sleep(200 * time.Millisecond)

	if w.TriggerSync(999995) {
		t.Error("TriggerSync succeeded on a fresh repo ID well after ctx cancellation — the exited-guard is not catching context cancellation")
	}

	w.Stop() // must still return promptly and without panicking afterward.
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
