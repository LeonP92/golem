package worker

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/ticket"
	"github.com/leonp92/golem/internal/workspace"
)

// Executor is the interface for running a ticket.
type Executor interface {
	RunTicket(ctx context.Context, cfg *config.Config, c *client.Client, claim *client.ClaimResponse) error
}

// Worker polls for available tickets, claims them, and runs them via Executor.
type Worker struct {
	cfg      *config.Config
	client   *client.Client
	executor Executor
	wsc      *client.WSClient
	stop     chan struct{}
	mu       sync.Mutex
	running  map[string]context.CancelFunc // ticketID → cancel for each active ticket
}

// New creates a new Worker. exec may be nil for testing (skips actual execution).
func New(cfg *config.Config, c *client.Client, exec Executor) *Worker {
	return &Worker{
		cfg:      cfg,
		client:   c,
		executor: exec,
		stop:     make(chan struct{}),
		running:  make(map[string]context.CancelFunc),
	}
}

// maxConcurrent returns the configured parallelism limit, defaulting to 1.
func (w *Worker) maxConcurrent() int {
	if w.cfg.MaxConcurrent > 0 {
		return w.cfg.MaxConcurrent
	}
	return 1
}

// SetWSClient attaches a WebSocket client for heartbeat pings.
func (w *Worker) SetWSClient(wsc *client.WSClient) {
	w.wsc = wsc
}

// Start registers with the orchestrator, resumes any in-progress tickets from
// before a restart, then begins the poll loop. It does not block.
func (w *Worker) Start() {
	repos := make([]string, len(w.cfg.Repos))
	for i, r := range w.cfg.Repos {
		repos[i] = r.NormalizedRemote
	}

	log.Printf("worker: registering with orchestrator (repos: %v)", repos)
	if _, err := w.client.Register(w.cfg.Name, repos); err != nil {
		log.Printf("worker: register error: %v", err)
	}

	// Log all assigned tickets so the operator knows the full picture on startup.
	assigned, assignedErr := w.client.GetAssigned()
	resumable, resumableErr := w.client.GetResumable()

	if assignedErr != nil {
		log.Printf("worker: get assigned error: %v", assignedErr)
	}
	if resumableErr != nil {
		log.Printf("worker: get resumable error: %v", resumableErr)
	}

	if len(assigned) == 0 {
		log.Printf("worker: no assigned tickets, waiting for work")
	} else {
		// Build a set of resumable IDs so we can label each ticket correctly.
		resumableIDs := make(map[string]bool, len(resumable))
		for _, r := range resumable {
			resumableIDs[r.TicketID] = true
		}
		log.Printf("worker: %d assigned ticket(s) on startup:", len(assigned))
		for _, t := range assigned {
			if resumableIDs[t.TicketID] {
				log.Printf("worker:   %s — phase: %s — will resume", t.TicketID, t.Phase)
			} else {
				log.Printf("worker:   %s — phase: %s — waiting (no action needed)", t.TicketID, t.Phase)
			}
		}
	}

	for _, claim := range resumable {
		go w.tryResumeTicket(claim)
	}

	log.Printf("worker: poll loop started")
	go w.pollLoop()
}

// tryResumeTicket resumes a ticket that was mid-execution when the shem last
// died. Unlike tryClaimAndRun it skips the claim step — the ticket is already
// owned by this shem.
func (w *Worker) tryResumeTicket(claim *client.ClaimResponse) {
	w.mu.Lock()
	if _, ok := w.running[claim.TicketID]; ok {
		w.mu.Unlock()
		return
	}
	if len(w.running) >= w.maxConcurrent() {
		w.mu.Unlock()
		log.Printf("worker: concurrency limit reached, skipping resume of ticket %s", claim.TicketID)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.running[claim.TicketID] = cancel
	w.mu.Unlock()

	log.Printf("worker: resuming ticket %s from checkpoint %q", claim.TicketID, *claim.CheckpointPhase)

	if err := w.client.PostPhase(claim.TicketID, *claim.CheckpointPhase); err != nil {
		log.Printf("worker: resume: failed to reset phase for ticket %s: %v", claim.TicketID, err)
	}

	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, claim.TicketID)
		w.mu.Unlock()
	}()

	if w.executor != nil {
		if err := w.executor.RunTicket(ctx, w.cfg, w.client, claim); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("worker: ticket %s resume error: %v", claim.TicketID, err)
			if phaseErr := w.client.PostPhase(claim.TicketID, "needs-attention"); phaseErr != nil {
				log.Printf("worker: post phase error: %v", phaseErr)
			}
		}
	}
}

// HandleMessage processes a WebSocket push message from the orchestrator.
func (w *Worker) HandleMessage(msg ws.WSMessage) {
	switch msg.Type {
	case "ticket_available":
		if msg.TicketID != nil {
			go w.tryClaimAndRun(*msg.TicketID)
		}
	case "ticket_requeued":
		if msg.TicketID != nil {
			log.Printf("worker: ticket %s requeued — stopping", *msg.TicketID)
			w.mu.Lock()
			if cancel, ok := w.running[*msg.TicketID]; ok {
				cancel()
			}
			w.mu.Unlock()
		}
	case "ticket_closed":
		if msg.TicketID != nil {
			log.Printf("worker: ticket %s closed — cleaning up", *msg.TicketID)
			w.mu.Lock()
			if cancel, ok := w.running[*msg.TicketID]; ok {
				cancel()
			}
			w.mu.Unlock()
			go w.cleanupTicket(msg.Repo, *msg.TicketID)
		}
	case "ticket_revise":
		if msg.TicketID != nil {
			go w.tryReviseAndRun(*msg.TicketID)
		}
	}
}

// cleanupTicket removes the local worktree and branch for a closed ticket.
func (w *Worker) cleanupTicket(repoRemote, ticketID string) {
	repoPath := repoLocalPath(w.cfg, repoRemote)
	if repoPath == "" {
		log.Printf("worker: cleanup ticket %s: no local path for repo %q", ticketID, repoRemote)
		return
	}
	ticketDir := filepath.Join(repoPath, ".golem", "tickets", ticketID)
	worktreePath := filepath.Join(ticketDir, "worktree")
	branch := "ticket/" + ticketID
	if s, err := ticket.Load(ticketDir); err == nil && s.Branch != "" {
		branch = s.Branch
	}
	if err := workspace.Remove(repoPath, worktreePath, branch); err != nil {
		log.Printf("worker: cleanup ticket %s: %v", ticketID, err)
	} else {
		log.Printf("worker: ticket %s cleaned up (worktree and branch removed)", ticketID)
	}
}

// tryClaimAndRun attempts to claim the ticket and run it.
// On 409 (ErrNotAvailable) it returns silently.
func (w *Worker) tryClaimAndRun(ticketID string) {
	w.mu.Lock()
	// Deduplicate: skip if this ticket is already running.
	if _, ok := w.running[ticketID]; ok {
		w.mu.Unlock()
		return
	}
	// Enforce concurrency limit.
	if len(w.running) >= w.maxConcurrent() {
		w.mu.Unlock()
		return
	}
	// Reserve a slot with a placeholder before releasing the lock.
	w.running[ticketID] = func() {}
	w.mu.Unlock()

	claim, err := w.client.ClaimTicket(ticketID)
	if err != nil {
		w.mu.Lock()
		delete(w.running, ticketID)
		w.mu.Unlock()
		if err != client.ErrNotAvailable {
			log.Printf("worker: claim ticket %s error: %v", ticketID, err)
		}
		return
	}

	log.Printf("worker: claimed ticket %s (%s)", ticketID, claim.RepoRemote)

	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.running[ticketID] = cancel
	w.mu.Unlock()

	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, ticketID)
		w.mu.Unlock()
	}()

	if w.executor != nil {
		if err := w.executor.RunTicket(ctx, w.cfg, w.client, claim); err != nil {
			if ctx.Err() != nil {
				log.Printf("worker: ticket %s stopped (context cancelled)", ticketID)
				return
			}
			log.Printf("worker: ticket %s error: %v", ticketID, err)
			if phaseErr := w.client.PostPhase(ticketID, "needs-attention"); phaseErr != nil {
				log.Printf("worker: post phase error: %v", phaseErr)
			}
		} else {
			log.Printf("worker: ticket %s finished", ticketID)
		}
	}
}

// tryReviseAndRun resumes a ticket already owned by this shem that has
// moved to revising (a human requested changes on ready-for-review).
// On 409 (ErrNotAvailable — e.g. requeued before this shem reconnected)
// it returns silently, same as tryClaimAndRun.
func (w *Worker) tryReviseAndRun(ticketID string) {
	w.mu.Lock()
	if _, ok := w.running[ticketID]; ok {
		w.mu.Unlock()
		return
	}
	if len(w.running) >= w.maxConcurrent() {
		w.mu.Unlock()
		return
	}
	w.running[ticketID] = func() {}
	w.mu.Unlock()

	claim, err := w.client.ClaimRevision(ticketID)
	if err != nil {
		w.mu.Lock()
		delete(w.running, ticketID)
		w.mu.Unlock()
		if err != client.ErrNotAvailable {
			log.Printf("worker: revise-claim ticket %s error: %v", ticketID, err)
		}
		return
	}

	log.Printf("worker: claimed revision for ticket %s (%s)", ticketID, claim.RepoRemote)

	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.running[ticketID] = cancel
	w.mu.Unlock()

	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, ticketID)
		w.mu.Unlock()
	}()

	if w.executor != nil {
		if err := w.executor.RunTicket(ctx, w.cfg, w.client, claim); err != nil {
			if ctx.Err() != nil {
				log.Printf("worker: ticket %s stopped (context cancelled)", ticketID)
				return
			}
			log.Printf("worker: ticket %s revise error: %v", ticketID, err)
			if phaseErr := w.client.PostPhase(ticketID, "needs-attention"); phaseErr != nil {
				log.Printf("worker: post phase error: %v", phaseErr)
			}
		} else {
			log.Printf("worker: ticket %s revision finished", ticketID)
		}
	}
}

// pollLoop polls for available tickets every 10 seconds.
func (w *Worker) pollLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			for _, r := range w.cfg.Repos {
				select {
				case <-w.stop:
					return
				default:
				}
				id, err := w.client.GetAvailable(r.NormalizedRemote)
				if err != nil {
					log.Printf("worker: get available error: %v", err)
					continue
				}
				if id != nil {
					go w.tryClaimAndRun(*id)
				}
			}
		}
	}
}

// Shutdown signals all running tickets to stop, waits up to 10 minutes for
// them to exit, then closes the stop channel and deregisters from the orchestrator.
func (w *Worker) Shutdown() {
	w.mu.Lock()
	for _, cancel := range w.running {
		cancel()
	}
	w.mu.Unlock()

	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		idle := len(w.running) == 0
		w.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	close(w.stop)

	if err := w.client.Deregister(); err != nil {
		log.Printf("worker: deregister error: %v", err)
	}
}
