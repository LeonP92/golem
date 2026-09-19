package ghsync

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// notifyBuffer bounds the number of distinct manual-trigger requests that can
// be queued for consumption before a caller of TriggerSync would block. Each
// repo can only occupy one outstanding slot at a time (see slot), so this
// only matters when triggers pile up across many distinct repos faster than
// ingestLoop can drain them.
const notifyBuffer = 64

// Worker runs the two independent loops of the sync engine.
//
// The intervals differ by an order of magnitude on purpose: ingest polls a
// mostly idle external system and is cheap to delay, while the drain reacts to
// events that already happened locally and must stay prompt — a 15-minute
// drain would make the retry backoff meaningless and delay every milestone
// comment.
type Worker struct {
	syncer        *Syncer
	pollInterval  time.Duration
	drainInterval time.Duration

	mu       sync.Mutex
	triggers map[uint]chan struct{} // repo ID → buffered(1) manual-trigger slot
	notify   chan uint              // repo IDs whose slot has been claimed

	stop     chan struct{} // closed by Stop to ask both loops to exit
	stopOnce sync.Once
	exited   chan struct{} // closed once either loop has actually returned, for any reason
	exitOnce sync.Once
	done     sync.WaitGroup
}

// NewWorker returns a Worker over syncer with the given intervals.
func NewWorker(s *Syncer, pollInterval, drainInterval time.Duration) *Worker {
	return &Worker{
		syncer:        s,
		pollInterval:  pollInterval,
		drainInterval: drainInterval,
		triggers:      map[uint]chan struct{}{},
		notify:        make(chan uint, notifyBuffer),
		stop:          make(chan struct{}),
		exited:        make(chan struct{}),
	}
}

// signalExited closes exited on the first call and is a no-op afterward. Both
// loops defer it so exited reflects "either loop has returned", regardless of
// whether that happened via Stop (w.stop) or via the caller's own ctx being
// cancelled — TriggerSync's guard needs to see both, not just the former (see
// TriggerSync).
func (w *Worker) signalExited() {
	w.exitOnce.Do(func() { close(w.exited) })
}

// slot returns the manual-trigger slot channel for a repo, creating it on
// first use. Buffered to 1 so at most one sync can be queued per repo: a
// second TriggerSync while one is already queued is a no-op (the queued pass
// will pick up the same work), which is exactly what lets TriggerSync report
// whether it actually queued anything.
func (w *Worker) slot(repoID uint) chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	ch, ok := w.triggers[repoID]
	if !ok {
		ch = make(chan struct{}, 1)
		w.triggers[repoID] = ch
	}
	return ch
}

// releaseSlot drains a repo's manual-trigger slot after its queued sync has
// been consumed, so a later TriggerSync for the same repo can queue again.
func (w *Worker) releaseSlot(repoID uint) {
	ch := w.slot(repoID)
	select {
	case <-ch:
	default:
		// Nothing to drain — defensive only; a repo ID never reaches here
		// without having gone through TriggerSync's successful claim first.
	}
}

// TriggerSync requests an immediate ingest of one repo. It claims that repo's
// size-1 slot and, only on success, publishes the repo ID to the shared
// notify channel that ingestLoop selects on. Both the slot claim and the
// notify publish are non-blocking: if notify is momentarily saturated (its
// buffer is bounded — see notifyBuffer) the claim is rolled back rather than
// left held with nothing to release it, so this can never strand the repo's
// slot or block the caller. It reports false when a sync is already queued
// for that repo, when the worker has already stopped or exited, or when
// notify was full, in which case the request is deliberately dropped — the
// queued or next scheduled pass will pick up the same work.
func (w *Worker) TriggerSync(repoID uint) bool {
	select {
	case <-w.stop:
		return false
	case <-w.exited:
		// Best-effort only: either channel can close concurrently with the
		// rest of this call, in which case the race below still applies. But
		// checking both here closes the common cases — w.stop for an
		// explicit Stop(), w.exited for either loop having already returned
		// via ctx cancellation with Stop() never called (the caller's ctx
		// was cancelled directly, which both loops also select on). Without
		// this, a trigger arriving after either exit would claim the slot,
		// report success, and never be released or processed, permanently
		// stranding the repo for the life of the process.
		return false
	default:
	}

	ch := w.slot(repoID)
	select {
	case ch <- struct{}{}:
	default:
		return false // a sync is already queued for this repo
	}
	select {
	case w.notify <- repoID:
		return true
	default:
		<-ch // release the claim: never leave a slot held with no notify sent
		return false
	}
}

// Start launches both loops. It does not block.
func (w *Worker) Start(ctx context.Context) {
	w.done.Add(2)
	go w.ingestLoop(ctx)
	go w.drainLoop(ctx)
}

// Stop signals both loops and waits for them to exit. It is safe to call more
// than once and from multiple goroutines.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
	w.done.Wait()
}

// ingestLoop runs a full pass on its ticker, and a single-repo pass whenever a
// manual trigger fires. Both paths call into ingestAll/ingestOne, which in
// turn call IngestRepo, so a manual sync and a scheduled one are the same
// code and can never overlap within this goroutine.
//
// The select below has exactly four cases and blocks between events — no
// timer fallthrough and no per-iteration channel construction. A trigger is
// carried on the shared notify channel only after TriggerSync has claimed
// that repo's own size-1 slot, so coalescing (a second trigger for the same
// repo is dropped while one is already queued) happens before this loop ever
// sees it.
func (w *Worker) ingestLoop(ctx context.Context) {
	defer w.done.Done()
	defer w.signalExited()
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	w.ingestAll(ctx) // one pass at startup so a restart is not a 15-minute gap

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-ticker.C:
			w.ingestAll(ctx)
		case repoID := <-w.notify:
			w.ingestOne(ctx, repoID)
			w.releaseSlot(repoID)
		}
	}
}

// ingestAll runs one ingest pass over every enabled repo.
func (w *Worker) ingestAll(ctx context.Context) {
	var repos []db.GitHubRepo
	if err := w.syncer.DB.Where("enabled = ?", true).Find(&repos).Error; err != nil {
		log.Printf("ghsync: list enabled repos: %v", err)
		return
	}
	for i := range repos {
		if err := w.syncer.IngestRepo(ctx, &repos[i]); err != nil {
			// recordRepoError already stored this; one repo never stops others.
			log.Printf("ghsync: ingest %s: %v", repos[i].RepoRemote, err)
		}
	}
}

// ingestOne runs an ingest pass for a single repo, used by manual sync.
func (w *Worker) ingestOne(ctx context.Context, repoID uint) {
	var repo db.GitHubRepo
	if err := w.syncer.DB.First(&repo, repoID).Error; err != nil {
		log.Printf("ghsync: manual sync load repo %d: %v", repoID, err)
		return
	}
	if !repo.Enabled {
		return
	}
	if err := w.syncer.IngestRepo(ctx, &repo); err != nil {
		log.Printf("ghsync: manual sync %s: %v", repo.RepoRemote, err)
	}
}

// drainLoop delivers queued GitHub writes on the fast ticker.
func (w *Worker) drainLoop(ctx context.Context) {
	defer w.done.Done()
	defer w.signalExited()
	ticker := time.NewTicker(w.drainInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stop:
			return
		case <-ticker.C:
			if err := w.syncer.Drain(ctx); err != nil {
				log.Printf("ghsync: drain: %v", err)
			}
		}
	}
}
