package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/ticket"
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

	// graphBuildMu serialises `golem graph build` runs on this shem. Two of
	// them over one checkout would fight over the graph index, and the
	// orchestrator's claim cannot prevent that on its own across a shem
	// restart.
	graphBuildMu sync.Mutex
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
			switch {
			case resumableIDs[t.TicketID]:
				log.Printf("worker:   %s — phase: %s — will resume", t.TicketID, t.Phase)
			case t.Phase == "revising":
				// reviseAssigned below picks these up. Saying "no action
				// needed" here was how the stranding looked in the log for
				// as long as it went unnoticed, and it would now be a lie.
				log.Printf("worker:   %s — phase: %s — will revise", t.TicketID, t.Phase)
			default:
				log.Printf("worker:   %s — phase: %s — waiting (no action needed)", t.TicketID, t.Phase)
			}
		}
	}

	for _, claim := range resumable {
		go w.tryResumeTicket(claim)
	}
	// A ticket already in revising is owed a revision, whether or not this
	// shem ever saw the ticket_revise push that started it. The push is
	// transient — sent once, lost to a restart or a dropped connection —
	// and nothing else recovered it: the poll loop asks only for AVAILABLE
	// tickets and resumableTickets excludes revising. Three real tickets
	// sat in that state reporting "waiting (no action needed)" until a
	// person noticed.
	//
	// The PHASE is the instruction; the push only makes it prompt.
	w.reviseAssigned(assigned)

	log.Printf("worker: poll loop started")
	go w.pollLoop()
}

// reviseAssigned starts a revision for every assigned ticket sitting in
// revising. Safe to call repeatedly: tryReviseAndRun skips a ticket already
// running here, and revise-claim refuses one this shem does not own or that
// has since moved on.
func (w *Worker) reviseAssigned(assigned []client.AssignedTicket) {
	for _, t := range assigned {
		if t.Phase == "revising" {
			go w.tryReviseAndRun(t.TicketID)
		}
	}
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

	// CheckpointPhase is nil when the shem died during the first phase, before
	// any checkpoint was written. Those tickets are resumable too — without
	// that they were unreachable: owned by a shem that would not resume them,
	// and in a phase that made them unclaimable by anyone else. Dereferencing
	// it unconditionally, as this did, would panic the shem on startup for
	// exactly the tickets the widened predicate now returns.
	//
	// There is no phase to reset to in that case: RunTicket's nil-checkpoint
	// branch starts at brainstorm and posts that phase itself.
	if claim.CheckpointPhase == nil {
		log.Printf("worker: resuming ticket %s from the start (no checkpoint)", claim.TicketID)
	} else {
		log.Printf("worker: resuming ticket %s from checkpoint %q", claim.TicketID, *claim.CheckpointPhase)
		if err := w.client.PostPhase(claim.TicketID, *claim.CheckpointPhase); err != nil {
			log.Printf("worker: resume: failed to reset phase for ticket %s: %v", claim.TicketID, err)
		}
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
			w.failTicket(claim.TicketID, fmt.Errorf("resuming the ticket: %w", err))
		}
	}
}

// failTicket records why a ticket stopped and then parks it in
// needs-attention.
//
// The phase change used to be the only trace: the error went to the shem's
// stdout via log.Printf and nowhere else, so the dashboard showed a red
// "Needs Attention" badge above an activity log whose last line was whatever
// step had been announced before the failure — "Initializing repository…",
// say. The reason existed, but only for someone with shell access to the
// shem host, and the one place a human is actually looking said nothing.
//
// BLOCKER rather than STATUS: this is the entry type the UI renders as a
// problem, and it is what the phase badge is claiming.
func (w *Worker) failTicket(ticketID string, cause error) {
	log.Printf("worker: ticket %s error: %v", ticketID, cause)
	if _, logErr := w.client.PostLog(ticketID, client.LogPayload{
		EntryType: "BLOCKER",
		FromRole:  "shem",
		Message:   cause.Error(),
	}); logErr != nil {
		// Best effort, and reported: if this fails the operator is back to
		// an unexplained badge, so the shem log should say why.
		log.Printf("worker: ticket %s: could not record the failure on the ticket: %v", ticketID, logErr)
	}
	if phaseErr := w.client.PostPhase(ticketID, "needs-attention"); phaseErr != nil {
		log.Printf("worker: post phase error: %v", phaseErr)
	}
}

// HandleMessage processes a WebSocket push message from the orchestrator.
func (w *Worker) HandleMessage(msg ws.WSMessage) {
	switch msg.Type {
	case "ticket_available":
		if msg.TicketID != nil {
			go w.tryClaimAndRun(*msg.TicketID)
		}
	case "graph_build":
		// Serialised per repository by graphBuildMu: the orchestrator claims
		// the slot before broadcasting, so a second request for the same repo
		// is refused there — but a shem restart could overlap with a
		// still-running build, and two `golem graph build` runs over one
		// checkout would fight over the index.
		if msg.Repo != "" {
			go w.runGraphBuild(msg.Repo)
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
	// Not workspace.Remove: that runs git as root, and this repo is the agent's.
	ctx := context.Background()
	if _, err := os.Stat(worktreePath); err == nil {
		if err := gitRun(ctx, repoPath, "worktree", "remove", worktreePath, "--force"); err != nil {
			log.Printf("worker: cleanup ticket %s: %v", ticketID, err)
			return
		}
	}
	_ = gitRun(ctx, repoPath, "branch", "-D", branch) // already gone is fine
	log.Printf("worker: ticket %s cleaned up (worktree and branch removed)", ticketID)
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
			w.failTicket(ticketID, err)
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
			w.failTicket(ticketID, fmt.Errorf("revising the ticket: %w", err))
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
			// Self-heal: a revision whose push was lost is picked up here
			// rather than waiting for a restart or a person.
			if assigned, err := w.client.GetAssigned(); err == nil {
				w.reviseAssigned(assigned)
			}
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

// runGraphBuild runs `golem graph build` for one repository and reports the
// outcome, which is what the repos view displays.
//
// The error text is the agent's own — agentrunner reports both of Claude's
// streams now — so a failure an operator can act on ("Failed to
// authenticate", a missing permission) reaches the page rather than an exit
// status.
func (w *Worker) runGraphBuild(remote string) {
	repoPath := repoLocalPath(w.cfg, remote)
	if repoPath == "" {
		w.reportGraphBuild(remote, fmt.Sprintf("this shem has no local path configured for %s", remote))
		return
	}

	w.graphBuildMu.Lock()
	defer w.graphBuildMu.Unlock()

	if err := agentenv.EnsureOwnership(repoPath); err != nil {
		log.Printf("worker: %v", err)
	}
	log.Printf("worker: building the code graph for %s in %s", remote, repoPath)
	out, err := asAgent(exec.Command("golem", "graph", "build", "--repo", repoPath)).CombinedOutput() //nolint:gosec
	if err != nil {
		// CombinedOutput rather than the error alone: `golem graph build`
		// prints why it failed and exits 1, so the exit status on its own
		// would tell an operator nothing.
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		log.Printf("worker: graph build for %s failed: %v\n%s", remote, err, msg)
		w.reportGraphBuild(remote, msg)
		return
	}
	log.Printf("worker: graph build for %s finished", remote)
	w.reportGraphBuild(remote, "")
}

// reportGraphBuild posts the outcome, and says so locally if it cannot. A
// dropped report leaves the row reading "building…" until it goes stale,
// which is recoverable but confusing, so it is worth a line in the log.
func (w *Worker) reportGraphBuild(remote, buildErr string) {
	if err := w.client.PostGraphBuildResult(remote, buildErr); err != nil {
		log.Printf("worker: could not report the graph build result for %s: %v", remote, err)
	}
}
