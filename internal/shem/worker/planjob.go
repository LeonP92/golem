package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/shem/client"
)

// Epic plan-generation jobs. One durable job is claimed like a ticket, run
// as a single agent session in the shem's local checkout (where the code
// graph and wiki already live), and completed with a draft plan or an
// error. The orchestrator re-validates the draft before storing it, and a
// human still saves it as a revision — how the JSON was produced changes
// nothing about the approval gate.

// planJobLogLimit bounds the session log tail posted to the orchestrator.
const planJobLogLimit = 4000

// planJobProgressInterval is how often a running session refreshes its
// lease; PlanJobStaleAfter on the orchestrator is the matching window.
const planJobProgressInterval = time.Minute

// tryClaimPlanJob attempts to claim and run a plan-generation job. On
// ErrPlanJobNotClaimable it returns silently, exactly like a lost ticket
// claim race.
func (w *Worker) tryClaimPlanJob(jobID string) {
	key := "planjob/" + jobID
	w.mu.Lock()
	if _, ok := w.running[key]; ok {
		w.mu.Unlock()
		return
	}
	if len(w.running) >= w.maxConcurrent() {
		w.mu.Unlock()
		return
	}
	w.running[key] = func() {}
	w.mu.Unlock()

	claim, err := w.client.ClaimPlanJob(jobID)
	if err != nil {
		w.mu.Lock()
		delete(w.running, key)
		w.mu.Unlock()
		if !errors.Is(err, client.ErrPlanJobNotClaimable) {
			log.Printf("worker: claim plan job %s error: %v", jobID, err)
		}
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.running[key] = cancel
	w.mu.Unlock()
	defer func() {
		cancel()
		w.mu.Lock()
		delete(w.running, key)
		w.mu.Unlock()
	}()

	w.runPlanJob(ctx, claim)
}

// runPlanJob runs the planning session (plus one repair pass when the
// first draft fails validation) and reports the outcome. Every exit path
// posts a result; a job that cannot run at all fails with the reason, so
// the epic page never shows a job that silently vanished.
func (w *Worker) runPlanJob(ctx context.Context, claim *client.PlanJobClaim) {
	jobID := claim.Job.ID
	log.Printf("worker: claimed plan job %s (epic %s)", jobID, claim.Job.EpicID)

	repoPath := repoLocalPath(w.cfg, claim.RepoRemote)
	if repoPath == "" {
		w.postPlanJobError(jobID, fmt.Sprintf("this shem has no local path configured for %s", claim.RepoRemote))
		return
	}
	planPath := filepath.Join(repoPath, ".golem", "epic-plan", jobID, "plan.json")
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		w.postPlanJobError(jobID, fmt.Sprintf("create plan directory: %v", err))
		return
	}
	// After the mkdir, so the plan directory is the agent account's too: the agent writes plan.json there.
	if err := agentenv.EnsureOwnership(repoPath); err != nil {
		log.Printf("worker: %v", err)
	}
	// The draft is read and posted before this runs; the checkout keeps no copy.
	defer func() { _ = os.RemoveAll(filepath.Dir(planPath)) }()

	logBuf := &tailWriter{max: planJobLogLimit}
	// Attempt 1. The progress post is what consumes the attempt; if the
	// budget is gone the server refuses, and the job fails with the reason
	// rather than running an uncounted session.
	if err := w.client.PostPlanJobProgress(jobID, "planning session starting", true); err != nil {
		w.postPlanJobError(jobID, fmt.Sprintf("plan job cannot start another session: %v", err))
		return
	}
	stopHeartbeat := w.planJobHeartbeat(ctx, jobID, logBuf)
	defer close(stopHeartbeat)

	repairErrors := ""
	for attempt := 1; ; attempt++ {
		prompt, err := buildEpicPlanPrompt(claim.EpicTitle, claim.SpecMarkdown, claim.BranchTarget, planPath, repairErrors)
		if err != nil {
			w.postPlanJobError(jobID, err.Error())
			return
		}
		model := w.agent.StageModel(models.StageEpicPlan)
		log.Printf("worker: plan job %s session %d starting (model %q)", jobID, attempt, model)
		runErr := w.agent.Adapter.RunPhase(ctx, repoPath, prompt, io.MultiWriter(os.Stdout, logBuf), model)
		if ctx.Err() != nil {
			log.Printf("worker: plan job %s stopped (context cancelled)", jobID)
			return
		}
		if runErr != nil {
			w.postPlanJobError(jobID, runErr.Error()+"\n"+logBuf.String())
			return
		}
		draft, valErrs := readAndValidatePlan(planPath, claim.BranchTarget)
		if valErrs == nil {
			if err := w.client.PostPlanJobResult(jobID, draft, ""); err != nil {
				log.Printf("worker: post plan job %s result error: %v", jobID, err)
			}
			log.Printf("worker: plan job %s finished", jobID)
			return
		}
		// The orchestrator owns the session budget (PlanJobMaxAttempts): it
		// refuses the new attempt once the repair pass is spent.
		repairErrors = errors.Join(valErrs...).Error()
		if err := w.client.PostPlanJobProgress(jobID, "plan failed validation; starting repair pass", true); err != nil {
			w.postPlanJobError(jobID, fmt.Sprintf("generated plan is invalid and no repair session is left (%v): %s", err, repairErrors))
			return
		}
	}
}

// planJobHeartbeat posts the session log tail every minute so the lease
// stays fresh over a long session and the epic page shows live output.
func (w *Worker) planJobHeartbeat(ctx context.Context, jobID string, logBuf *tailWriter) chan struct{} {
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(planJobProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.client.PostPlanJobProgress(jobID, logBuf.String(), false); err != nil {
					log.Printf("worker: plan job %s progress error: %v", jobID, err)
				}
			}
		}
	}()
	return stop
}

// postPlanJobError fails the job with reason; a post failure is logged,
// not retried — the stale-lease reaper gives the job another owner.
func (w *Worker) postPlanJobError(jobID, reason string) {
	if err := w.client.PostPlanJobResult(jobID, "", reason); err != nil {
		log.Printf("worker: post plan job %s failure (%q) error: %v", jobID, reason, err)
	}
}

// readAndValidatePlan applies the same rules the orchestrator applies to a
// hand-pasted plan (epic.CanonicalPlan is the one gate). The shem catches
// bad output before posting; the orchestrator re-checks regardless.
func readAndValidatePlan(planPath, branchTarget string) (string, []error) {
	data, err := os.ReadFile(planPath) //nolint:gosec
	if err != nil {
		return "", []error{fmt.Errorf("the planning session did not write %s: %w", planPath, err)}
	}
	return epic.CanonicalPlan(data, branchTarget)
}
