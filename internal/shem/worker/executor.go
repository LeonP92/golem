package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/forge"
	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"gopkg.in/yaml.v3"
)

// GolemExecutor runs tickets phase by phase. Per ticket, the spec and the plan
// are either gated by a human (manual approval) or by automatic validation.
type GolemExecutor struct {
	repoMu      sync.Map // keyed by repoPath, value *sync.Mutex
	integrating sync.Map // active ticket IDs; polling skips an in-flight lane
	Agent       *Agent
	// Forge merges a finished epic child into its run's target ref (plain
	// git in the push mirror). Nil means forge.Git{}; tests substitute a
	// fake. The worker depends on the operation, not on the git CLI.
	Forge forge.Forge
}

func (e *GolemExecutor) repoMutex(repoPath string) *sync.Mutex {
	v, _ := e.repoMu.LoadOrStore(repoPath, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// initRepo acquires the per-repo mutex, runs ensureRepoReady, and releases the
// mutex before returning. Using a helper keeps the defer scope tight.
func (e *GolemExecutor) initRepo(ctx context.Context, repoPath, graphModel string) error {
	mu := e.repoMutex(repoPath)
	mu.Lock()
	defer mu.Unlock()
	return e.ensureRepoReady(ctx, repoPath, graphModel)
}

// RunTicket executes a claimed ticket:
//  1. Pre-creates the local ticket with `golem ticket new` (scaffolding only, no agent).
//  2. Ensures the repo is initialized and the code graph is current.
//  3. Runs three separate agent sessions — brainstorm, plan, implement —
//     pausing between brainstorm→plan and plan→implement for human approval via the
//     orchestrator UI.  Tickets are NOT closed automatically; humans close via the UI.
func (e *GolemExecutor) RunTicket(ctx context.Context, cfg *config.Config, c *client.Client, claim *client.ClaimResponse) error {
	repoPath := repoLocalPath(cfg, claim.RepoRemote)
	if repoPath == "" {
		return fmt.Errorf("no local path for repo %q", claim.RepoRemote)
	}

	// Cloned here, not only on the checkpoint-resume path. CloneIfMissing was
	// reachable solely from RecoverTicket, so a repository the shem had never
	// seen was never cloned for a FRESH ticket — which is every first ticket
	// after adding a repository to the shem's config. The directory existed
	// (the /repos volume mount), so nothing complained until `golem ticket
	// new` reached `git worktree add` and failed with "not a git repository".
	//
	// Fatal rather than a warning, unlike the pre-flight below: without a
	// repository on disk every later step fails, and doing so here names the
	// cause instead of leaving a git exit status to explain it.
	if err := CloneIfMissing(ctx, repoPath, repoCloneRemote(cfg, claim.RepoRemote)); err != nil {
		return fmt.Errorf("preparing %s: %w", repoPath, err)
	}

	// Right after the clone: every later repo command runs as the agent
	// (asAgent) and needs to own the repo. No-op without an agent account.
	if err := agentenv.EnsureOwnership(repoPath); err != nil {
		log.Printf("executor: %v", err)
	}

	// Before initRepo, which runs `golem graph build` and therefore invokes
	// the agent: an untrusted workspace makes Claude Code ignore the
	// repository's own permission allow-list.
	//
	// Failure is logged and not returned: an untrusted workspace degrades (the
	// agent warns and falls back to asking) rather than breaking, so it is not
	// worth failing a ticket that would otherwise run.
	if err := e.Agent.Adapter.PrepareHost(repoPath); err != nil {
		log.Printf("executor: could not prepare the host for %s: %v", repoPath, err)
	}

	if err := e.initRepo(ctx, repoPath, e.claimModel(c, claim, models.StageGraph)); err != nil {
		log.Printf("executor: repo pre-flight warning: %v", err)
	}

	ticketID := claim.TicketID
	ticketDir := filepath.Join(repoPath, ".golem", "tickets", ticketID)
	logPath := filepath.Join(ticketDir, "log.jsonl")

	if cfg.NoPush || strings.HasPrefix(claim.RepoRemote, "local://") {
		setPushURL(repoPath, repoPath)
		defer setPushURL(repoPath, "")
	}

	// Determine starting phase and ensure the ticket exists locally.
	startPhase := "brainstorm"
	if claim.RetryPhase != "" {
		postStatus(c, ticketID, "Retrying "+claim.RetryPhase+" from the saved branch checkpoint")
		startPhase = retryWorkerPhase(claim.RetryPhase)
		if startPhase == "" {
			return fmt.Errorf("unsupported retry phase %q", claim.RetryPhase)
		}
		if claim.CheckpointPhase != nil && claim.CheckpointSHA != nil {
			recoveryClaim := *claim
			if startPhase == "revising" {
				recoveryClaim.CheckpointPhase = &startPhase
			}
			if err := RecoverTicketForRetry(ctx, &recoveryClaim, cfg, e.Agent.Adapter.WorktreeSetup); err != nil {
				return fmt.Errorf("retry recovery failed: %w", err)
			}
		}
		if startPhase != "revising" {
			if err := runGolemAdvance(ctx, repoPath, ticketID, startPhase); err != nil {
				return fmt.Errorf("restore local ticket phase for retry: %w", err)
			}
		}
		if err := c.PostPhase(ticketID, startPhase); err != nil {
			if errors.Is(err, client.ErrNotOwner) {
				return nil
			}
			return fmt.Errorf("set retry phase: %w", err)
		}
	} else if claim.CheckpointPhase != nil {
		postStatus(c, ticketID, "Resuming from checkpoint: "+*claim.CheckpointPhase)
		if err := RecoverTicket(ctx, claim, cfg, e.Agent.Adapter.WorktreeSetup); err != nil {
			return fmt.Errorf("recovery failed: %w", err)
		}
		startPhase = *claim.CheckpointPhase
		// A checkpoint records a phase that COMPLETED and PASSED its gate:
		// brainstorm and plan are checkpointed only after validation, and in
		// manual-approval mode only after a human approved. So the next phase
		// always starts; an approval still open for that next phase is picked
		// up by gatedPhase.
		if startPhase == "brainstorm" || startPhase == "plan" {
			startPhase = nextPhaseAfterCheckpoint(startPhase)
			postStatus(c, ticketID, "Resuming validated "+*claim.CheckpointPhase+" — continuing at "+startPhase)
		}
	} else {
		// Fresh orchestrator ticket (no checkpoint). Always start from brainstorm.
		// If the ticket directory already exists (e.g. after a requeue) reuse the
		// existing worktree instead of trying to create it again.
		postStatus(c, ticketID, "Initializing repository…")
		if _, statErr := os.Stat(ticketDir); os.IsNotExist(statErr) {
			baseFallback, accept := "", []string(nil)
			if claim.Epic != nil {
				baseFallback, accept = claim.Epic.BaseFallback, claim.Epic.AcceptanceCommands
			}
			if err := e.runGolemTicketNew(ctx, repoPath, ticketID, claim.Branch, claim.Description, baseFetchRemote(cfg.NoPush, claim.RepoRemote), claim.BaseBranch, baseFallback, accept); err != nil {
				return fmt.Errorf("golem ticket new: %w", err)
			}
		}
		if phErr := c.PostPhase(claim.TicketID, "brainstorm"); phErr != nil {
			if errors.Is(phErr, client.ErrNotOwner) {
				log.Printf("executor: ticket %s was requeued, stopping", claim.TicketID)
				return nil
			}
			log.Printf("executor: post phase error: %v", phErr)
		}
	}

	// Start log-tail before any agent invocations so we capture all entries.
	stopTail := make(chan struct{})
	tailDone := make(chan struct{})
	go func() {
		defer close(tailDone)
		tailLog(logPath, claim.TicketID, len(claim.LogEntries), c, stopTail)
	}()
	defer func() {
		close(stopTail)
		<-tailDone
	}()

	// Phase loop: brainstorm → plan → implement. Validation gates each step; a
	// ticket in manual-approval mode also waits for a human after brainstorm
	// and after plan (gatedPhase). A failed validation parks the ticket in
	// needs-attention rather than waiting.
	for phase := startPhase; ; {
		switch phase {
		case "brainstorm", "plan":
			if stop, err := e.runDesignPhase(ctx, c, claim, repoPath, ticketDir, designPhases[phase]); err != nil {
				return err
			} else if stop {
				return nil
			}
			phase = nextPhaseAfterCheckpoint(phase)

		case "implement":
			model := e.claimModel(c, claim, models.StageImplement)
			postPhaseStart(c, ticketID, e.Agent.Adapter.Name(), model, "implement")
			postCurrentHeadCheckpoint(ctx, c, ticketID, "implement", filepath.Join(ticketDir, "worktree"))
			feedback := consumeFeedback(ctx, c, ticketID)
			prompt := withHumanFeedback(buildImplementPrompt(ticketID, claim.Branch, claim.Description), feedback)
			if err := e.runPhase(ctx, repoPath, prompt, ticketDir, "implement", model); err != nil {
				return err
			}
			e.observe(ctx, c, repoPath, ticketDir, ticketID, claim.BaseBranch, e.claimModel(c, claim, models.StageObserve))
			e.selfVerify(ctx, c, claim, repoPath, ticketDir, ticketID, model)
			if revErr := e.runGolemReview(ctx, repoPath, ticketID, claim.BaseBranch, e.claimModel(c, claim, models.StageReview)); revErr != nil {
				postStatus(c, ticketID, "Review gate failed to run: "+firstLineOf(revErr.Error()))
				log.Printf("executor: review gate for %s: %v", ticketID, revErr)
			}
			return e.finishWorkPhase(ctx, cfg, c, claim, repoPath, ticketDir, "Implementation", claim.Branch)

		case "revising":
			feedback := consumeFeedback(ctx, c, claim.TicketID)
			postCurrentHeadCheckpoint(ctx, c, ticketID, "revising", filepath.Join(ticketDir, "worktree"))
			// Make origin/<base> current before the agent runs. A revision
			// asked for by the pull-request monitor may be "this no longer
			// merges into main", and the fix is `git merge origin/main` —
			// which needs that ref to exist locally and to be up to date.
			//
			// The agent cannot fetch it itself: it runs token-free, so on a
			// private repository the fetch would simply fail auth. This
			// routes it the way recover.go already does — root fetches into
			// the Golem-owned mirror with the credential, bundles it, and
			// the agent fetches from the bundle with no network at all.
			//
			// Best effort: a ticket whose revision is about something else
			// must not fail because the base could not be refreshed.
			if claim.BaseBranch != "" {
				if err := fetchBranchForAgent(ctx, repoPath, claim.RepoRemote, claim.BaseBranch); err != nil {
					log.Printf("executor: refresh origin/%s for %s: %v", claim.BaseBranch, ticketID, err)
				}
			}
			model := e.claimModel(c, claim, models.StageRevise)
			postPhaseStart(c, ticketID, e.Agent.Adapter.Name(), model, "revise")
			if err := e.runPhase(ctx, repoPath, buildRevisePrompt(ticketID, claim.Branch, claim.Description, feedback), ticketDir, "revise", model); err != nil {
				return err
			}
			e.observe(ctx, c, repoPath, ticketDir, ticketID, claim.BaseBranch, e.claimModel(c, claim, models.StageObserve))
			e.selfVerify(ctx, c, claim, repoPath, ticketDir, ticketID, model)
			if revErr := e.runGolemReview(ctx, repoPath, ticketID, claim.BaseBranch, e.claimModel(c, claim, models.StageReview)); revErr != nil {
				postStatus(c, ticketID, "Review gate failed to run: "+firstLineOf(revErr.Error()))
				log.Printf("executor: review gate for %s: %v", ticketID, revErr)
			}
			// Previously defaulted an unreadable state to "ready-for-review",
			// so a revise run that left no state reported success outright.
			return e.finishWorkPhase(ctx, cfg, c, claim, repoPath, ticketDir, "Revision", claim.Branch)

		default:
			log.Printf("executor: unknown start phase %q, falling through to implement", phase)
			phase = "implement"
		}
	}
}

// golemTicketNewArgs builds the argv for `golem ticket new`.
//
// The "--" before the description is load-bearing, not cosmetic. The
// description is a GitHub issue body an untrusted third party wrote, and
// TicketNew hands its arguments to a flag.FlagSet, which treats anything
// starting with "-" as a flag. Without the separator:
//
//   - Any ordinary markdown body opening with a bullet, a "- [ ]" checklist,
//     a "---" rule or a "---" front-matter block fails to parse ("flag
//     provided but not defined" / "bad flag syntax"), the worker posts
//     needs-attention, and a human requeue fails identically — a permanent
//     loop that anyone who can open an issue on a watched repo can trigger.
//   - A body of exactly "--from-issue=N" parses as that flag and takes the
//     GitHub-fetch branch, so on a co-located orchestrator+shem box (where
//     GOLEM_GITHUB_TOKEN is in the environment) the shem would fetch an
//     arbitrary, never-approved issue and use it as the description.
//
// Go's flag package stops parsing flags at a bare "--" and returns
// everything after it from fs.Args(), which is what makes one argument close
// both cases. See TestGolemTicketNewArgs_SeparatesDescription here and
// TestTicketNew_DescriptionStartingWithDash in internal/cli, which proves
// the real FlagSet honours it.
func golemTicketNewArgs(ticketID, branch, description, backendConfig, baseSHA string, accept ...string) []string {
	args := []string{"ticket", "new", "--ticket-id", ticketID, "--branch", branch,
		"--backend-config", backendConfig}
	// --base precedes the separator: it is resolved locally from git
	// (never from the untrusted description), so it is safe in the flag
	// region the separator protects.
	if baseSHA != "" {
		args = append(args, "--base", baseSHA)
	}
	for _, c := range accept {
		args = append(args, "--accept", c)
	}
	return append(args, "--", description)
}

// runGolemTicketNew creates the local ticket scaffold (worktree + branch) without
// invoking an agent. The agent's role starts at brainstorm, after the scaffold exists.
func (e *GolemExecutor) runGolemTicketNew(ctx context.Context, repoPath, ticketID, branch, description, remote, baseBranch, baseFallback string, accept []string) error {
	baseSHA := ""
	if baseBranch != "" {
		sha, err := resolveBaseSHA(ctx, repoPath, remote, baseBranch, baseFallback)
		if err != nil {
			return err
		}
		baseSHA = sha
	}
	cmd := asGolemCLI(exec.CommandContext(ctx, "golem",
		golemTicketNewArgs(ticketID, branch, description, e.Agent.ConfigPath, baseSHA, accept...)...))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	log.Printf("executor: created ticket %s in %s", ticketID, repoPath)
	return nil
}

// resolveBaseSHA pins a fresh ticket's base to an exact commit. When the
// base cannot be resolved, an epic child falls back to the run's base
// branch (its integration ref does not exist until the first child
// creates it) and an ordinary ticket starts from HEAD, as before pinning.
func resolveBaseSHA(ctx context.Context, repoPath, remote, baseBranch, epicFallback string) (string, error) {
	sha, err := resolveClaimBaseSHA(ctx, repoPath, remote, baseBranch)
	if err == nil {
		return sha, nil
	}
	if epicFallback == "" {
		log.Printf("executor: base %s unresolvable (%v); starting from HEAD", baseBranch, err)
		return revParse(ctx, repoPath, "HEAD")
	}
	log.Printf("executor: base %s unresolvable; resolving from %s", baseBranch, epicFallback)
	sha, ferr := resolveClaimBaseSHA(ctx, repoPath, remote, epicFallback)
	if ferr != nil {
		return "", fmt.Errorf("resolving base %s (fallback for %s): %w", epicFallback, baseBranch, ferr)
	}
	return sha, nil
}

// baseFetchRemote is the remote a fresh ticket fetches its base from; "" when
// there is none (no_push, local://), so the base resolves from local refs.
func baseFetchRemote(noPush bool, remote string) string {
	if noPush || strings.HasPrefix(remote, "local://") {
		return ""
	}
	return remote
}

// resolveClaimBaseSHA fetches baseBranch (when there is a remote) and
// returns origin/<base>, else the local branch.
func resolveClaimBaseSHA(ctx context.Context, repoPath, remote, baseBranch string) (string, error) {
	if remote != "" {
		if err := fetchBranchForAgent(ctx, repoPath, remote, baseBranch); err != nil {
			return "", err
		}
	}
	for _, ref := range []string{"refs/remotes/origin/" + baseBranch, baseBranch} {
		if sha, err := revParse(ctx, repoPath, ref); err == nil {
			return sha, nil
		}
	}
	return "", fmt.Errorf("no commit found for %q locally or on origin", baseBranch)
}

// revParse resolves ref to a commit SHA, as the agent.
func revParse(ctx context.Context, dir, ref string) (string, error) {
	out, err := asAgent(exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--verify", ref+"^{commit}")).Output()
	return strings.TrimSpace(string(out)), err
}

// runGolemAdvance advances the local ticket to the given phase.
func runGolemAdvance(ctx context.Context, repoPath, ticketID, toPhase string) error {
	cmd := asGolemCLI(exec.CommandContext(ctx, "golem", "ticket", "advance", "--ticket", ticketID, "--to", toPhase))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// runGolemReview runs the review gate — the configured gate commands, then the
// reviewer agent — and is what advances the local ticket to
// ready-for-review.
//
// Run by the SHEM, not by the agent, for two reasons.
//
// It could not work from the agent. Claude Code runs the agent's shell
// commands in a sandbox that does not expose the environment, so `golem`
// started from there has no model credential, and the nested agent the
// reviewer needs reports "Not logged in · Please run /login". The shem runs it
// via asAgent, which keeps the model credential but not the push token.
//
// And it should not have been the agent's job anyway. This is the gate that
// decides whether work is fit to review; leaving it to the agent to remember
// meant a gate the system relies on could be skipped, and nothing but the
// agent's own word said it had run.
// runGolemValidate runs the automatic gate that replaced the human approval
// step after brainstorm and after plan.
//
// Exit code 2 means the spec-adherence role raised a blocker, i.e. the stage
// did not pass; any other non-zero exit means the gate could not run. Both
// are returned as errors, and both park the ticket in needs-attention — a
// gate that cannot run must not be treated as a pass, or the failure mode of
// the validator is "everything is approved".
func (e *GolemExecutor) runGolemValidate(ctx context.Context, repoPath, ticketID, stage, model string) error {
	args := e.Agent.subcommandArgs([]string{"ticket", "validate",
		"--ticket", ticketID, "--stage", stage}, model)
	cmd := asGolemCLI(exec.CommandContext(ctx, "golem", args...))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// selfVerify runs the ticket's gate and acceptance commands (`golem
// ticket verify`) before the review gate and, when a check fails, gives
// the implementer one pass to fix it. The review gate reruns the same
// commands either way. Every ticket verifies by default; a ticket
// created with verify_disabled skips it.
func (e *GolemExecutor) selfVerify(ctx context.Context, c *client.Client,
	claim *client.ClaimResponse, repoPath, ticketDir, ticketID, model string) {
	if claim.VerifyDisabled {
		return
	}
	out, failed := e.runGolemVerify(ctx, repoPath, ticketID)
	if !failed {
		return
	}
	postStatus(c, ticketID, "Self-verification failed; the implementer gets one pass to fix it")
	if err := e.runPhase(ctx, repoPath,
		buildImplementPrompt(ticketID, claim.Branch, claim.Description)+
			"\n\nYour own verification just failed with this output — fix the cause before anything else:\n"+out,
		ticketDir, "implement", model); err != nil {
		log.Printf("executor: self-verify fix pass for %s: %v", ticketID, err)
		return
	}
	if _, failed := e.runGolemVerify(ctx, repoPath, ticketID); failed {
		postStatus(c, ticketID, "Self-verification still failing after the fix pass; the review gate decides")
	}
}

// runGolemVerify reports failed only when a check failed (exit 2), not
// when verification could not run.
func (e *GolemExecutor) runGolemVerify(ctx context.Context, repoPath, ticketID string) (string, bool) {
	cmd := asGolemCLI(exec.CommandContext(ctx, "golem", "ticket", "verify", "--ticket", ticketID))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	failed := errors.As(err, &exit) && exit.ExitCode() == 2
	if err != nil && !failed {
		log.Printf("executor: self-verification for %s did not run: %v\n%s", ticketID, err, out)
	}
	return string(out), failed
}

func (e *GolemExecutor) runGolemReview(ctx context.Context, repoPath, ticketID, baseBranch, model string) error {
	args := []string{"ticket", "review", "--ticket", ticketID}
	if baseBranch != "" {
		args = append(args, "--base", "origin/"+baseBranch)
	}
	args = e.Agent.subcommandArgs(args, model)
	cmd := asGolemCLI(exec.CommandContext(ctx, "golem", args...))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// asAgent runs a repository command as the agent account without golem's
// credentials. The agent controls the repo (hooks, fsmonitor, gate commands),
// so running there as root would leak the push token. Only the mirror push
// in pushTicketBranch keeps root.
func asAgent(cmd *exec.Cmd) *exec.Cmd {
	cmd.Env = agentenv.Environ()
	agentenv.DropPrivileges(cmd)
	return cmd
}

// asGolemCLI retains the operator's agent-env policy for Golem's own nested
// CLI so verification and review gates can apply it again to their children.
// The policy is not passed to agent or repository commands.
func asGolemCLI(cmd *exec.Cmd) *exec.Cmd {
	cmd.Env = agentenv.GolemCLIEnviron()
	agentenv.DropPrivileges(cmd)
	return cmd
}

// consumeFeedback retrieves the pending feedback for a ticket and acks it so
// the shem doesn't re-read it on a restart. Returns "" if none.
func consumeFeedback(ctx context.Context, c *client.Client, ticketID string) string {
	fb, err := c.GetPendingFeedback(ticketID)
	if err != nil || fb == nil {
		return ""
	}
	if err := c.AckInput(ticketID, fb.ID); err != nil {
		log.Printf("executor: ack feedback %d: %v", fb.ID, err)
	}
	return fb.Prompt
}

func retryWorkerPhase(phase string) string {
	switch phase {
	case "brainstorm", "plan", "implement", "revising":
		return phase
	default:
		return ""
	}
}

func withHumanFeedback(prompt, feedback string) string {
	if strings.TrimSpace(feedback) == "" {
		return prompt
	}
	return prompt + "\n\nHuman retry feedback:\n" + feedback
}

func postCurrentHeadCheckpoint(ctx context.Context, c *client.Client, ticketID, phase, worktree string) {
	sha, err := revParse(ctx, worktree, "HEAD")
	if err != nil {
		log.Printf("executor: checkpoint %s at %s: %v", ticketID, phase, err)
		return
	}
	PostCheckpointWithRetry(c, ticketID, phase, sha, 5) //nolint:errcheck
}

// postDocumentEntry streams filePath to the orchestrator as a document log entry.
// If the file is missing or empty it is a no-op.
func postDocumentEntry(c *client.Client, ticketID string, entryType, filePath string) {
	info, err := os.Stat(filePath) //nolint:gosec
	if err != nil || info.Size() == 0 {
		return
	}
	if _, postErr := c.PostDocumentFile(ticketID, entryType, "developer", filePath); postErr != nil {
		log.Printf("executor: post %s entry: %v", entryType, postErr)
	}
}

// designPhase describes the brainstorm and plan phases, which share one gated
// shape: run the agent, adopt the document it wrote, validate it, and — in
// manual-approval mode — wait for a human before advancing to the next phase.
type designPhase struct {
	name         string // phase, checkpoint and stage label
	document     string // file the agent writes
	docKind      string // log-entry kind for the document
	validateKind string // argument to golem validate
	gatePrompt   string // approval request text in manual-approval mode
	validating   string // status posted while validation runs
	cleared      string // status posted after the gate passes
	buildPrompt  func(ticketID, description, feedback string) string
}

var designPhases = map[string]designPhase{
	"brainstorm": {
		name: "brainstorm", document: "spec.md", docKind: "SPEC", validateKind: "spec",
		gatePrompt:  "Brainstorm complete. Review the spec and approve to continue to planning.",
		validating:  "Brainstorm complete — validating the spec",
		cleared:     "Spec cleared — advancing to planning",
		buildPrompt: buildBrainstormPrompt,
	},
	"plan": {
		name: "plan", document: "plan.md", docKind: "PLAN", validateKind: "plan",
		gatePrompt:  "Plan complete. Review the plan and approve to begin implementation.",
		validating:  "Plan complete — validating the plan",
		cleared:     "Plan cleared — starting implementation",
		buildPrompt: buildPlanPrompt,
	},
}

// runDesignPhase executes one gated design phase and advances the ticket to
// the next phase. stop reports that the ticket was requeued mid-phase and the
// caller must return without error.
func (e *GolemExecutor) runDesignPhase(ctx context.Context, c *client.Client,
	claim *client.ClaimResponse, repoPath, ticketDir string, dp designPhase) (stop bool, err error) {
	ticketID := claim.TicketID
	out, gErr := gatedPhase(ctx, c, ticketID, claim.ManualApproval,
		dp.gatePrompt,
		func(m string) { postStatus(c, ticketID, m) },
		func() error {
			feedback := consumeFeedback(ctx, c, claim.TicketID)
			model := e.claimModel(c, claim, models.Stage(dp.name))
			postPhaseStart(c, ticketID, e.Agent.Adapter.Name(), model, dp.name)
			prompt := dp.buildPrompt(ticketID, claim.Description, feedback)
			if err := e.runPhase(ctx, repoPath, prompt, ticketDir, dp.name, model); err != nil {
				return err
			}
			adoptStrayTicketFile(ticketDir, dp.document)
			postDocumentEntry(c, claim.TicketID, dp.docKind, filepath.Join(ticketDir, dp.document))
			postStatus(c, ticketID, dp.validating)
			if err := e.runGolemValidate(ctx, repoPath, ticketID, dp.validateKind, e.claimModel(c, claim, models.StageValidate)); err != nil {
				return fmt.Errorf("%s validation: %w", dp.validateKind, err)
			}
			return nil
		})
	if gErr != nil {
		postCurrentHeadCheckpoint(ctx, c, ticketID, dp.name, filepath.Join(ticketDir, "worktree"))
		return false, gErr
	}
	if out == approvalGone {
		log.Printf("executor: ticket %s was requeued, stopping", ticketID)
		return true, nil
	}
	postStatus(c, ticketID, dp.cleared)
	next := nextPhaseAfterCheckpoint(dp.name)
	PostCheckpointWithRetry(c, claim.TicketID, dp.name, "", 5) //nolint:errcheck
	if err := runGolemAdvance(ctx, repoPath, ticketID, next); err != nil {
		log.Printf("executor: advance to %s: %v", next, err)
	}
	if phErr := c.PostPhase(claim.TicketID, next); phErr != nil {
		if errors.Is(phErr, client.ErrNotOwner) {
			log.Printf("executor: ticket %s was requeued, stopping", ticketID)
			return true, nil
		}
		log.Printf("executor: post phase error: %v", phErr)
	}
	return false, nil
}

// toOrchestratorPhase maps the local golem state.json phase to the orchestrator
// phase name. "review" and "closed" both become "ready-for-review" because humans
// close tickets via the UI — the shem never auto-closes.
func toOrchestratorPhase(localPhase string) string {
	switch localPhase {
	case "review", "closed":
		return "ready-for-review"
	default:
		return localPhase
	}
}

// nextPhaseAfterCheckpoint returns the phase to execute after a completed
// checkpoint phase. Brainstorm and plan are checkpointed only once their gate
// (validation, plus a human approval in manual mode) is passed, so the next
// run can skip ahead.
func nextPhaseAfterCheckpoint(phase string) string {
	switch phase {
	case "brainstorm":
		return "plan"
	case "plan":
		return "implement"
	default:
		return phase
	}
}

// ensureRepoReady verifies the repo has a .golem setup and a code graph.
func (e *GolemExecutor) ensureRepoReady(ctx context.Context, repoPath, graphModel string) error {
	graphArgs := func(sub string) []string {
		return e.Agent.subcommandArgs([]string{"graph", sub, "--repo", repoPath}, graphModel)
	}
	configPath := filepath.Join(repoPath, ".golem", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		out, err := asGolemCLI(exec.CommandContext(ctx, "golem", "init",
			"--backend", e.Agent.Adapter.Name(), "--repo", repoPath)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("golem init: %w\n%s", err, out)
		}
		log.Printf("executor: ran golem init in %s", repoPath)
	}

	// This repo is orchestrator-managed: the orchestrator is the only writer
	// to GitHub. Two writers would post duplicate comments and fight over
	// labels. Pin this on every pre-flight (not only right after golem
	// init) so a config that predates this feature, or one a human edited
	// by hand, is also brought back in line.
	if err := setGitHubWrite(configPath, false); err != nil {
		log.Printf("executor: could not pin github.write=false in %s: %v", configPath, err)
	}

	indexPath := filepath.Join(repoPath, ".golem", "index")
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		out, err := asGolemCLI(exec.CommandContext(ctx, "golem", graphArgs("build")...)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("golem graph build: %w\n%s", err, out)
		}
		log.Printf("executor: built graph in %s", repoPath)
	} else {
		out, err := asGolemCLI(exec.CommandContext(ctx, "golem", graphArgs("update")...)).CombinedOutput()
		if err != nil {
			log.Printf("executor: graph update failed, rebuilding: %v\n%s", err, out)
			out, err = asGolemCLI(exec.CommandContext(ctx, "golem", graphArgs("build")...)).CombinedOutput()
			if err != nil {
				return fmt.Errorf("golem graph build: %w\n%s", err, out)
			}
			log.Printf("executor: rebuilt graph in %s", repoPath)
		} else {
			log.Printf("executor: updated graph in %s", repoPath)
		}
	}
	return nil
}

// setGitHubWrite rewrites the github.write key in the YAML config at path to
// write, preserving every other key (gate.commands, role_models, etc.) by
// round-tripping through a generic map rather than the typed Config struct,
// which would silently drop any key it doesn't know about.
func setGitHubWrite(path string, write bool) error {
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	githubBlock, _ := doc["github"].(map[string]any)
	if githubBlock == nil {
		githubBlock = map[string]any{}
	}
	githubBlock["write"] = write
	doc["github"] = githubBlock

	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil { //nolint:gosec
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// tailLog polls logPath every 500 ms, forwarding any new lines to the orchestrator.
// skipLines is the number of lines already stored server-side (from ClaimResponse.LogEntries).
func tailLog(logPath string, ticketID string, skipLines int, c *client.Client, stop <-chan struct{}) {
	forwarded := skipLines

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			// Drain remaining lines before exiting.
			forwardNewLines(logPath, ticketID, &forwarded, c)
			return
		case <-ticker.C:
			forwardNewLines(logPath, ticketID, &forwarded, c)
		}
	}
}

// rawBlogEntry matches the JSON schema written by blog.Writer (role, type, message).
type rawBlogEntry struct {
	Role    string `json:"role"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// forwardNewLines opens logPath, skips *forwarded lines, and POSTs any new ones.
// *forwarded always advances past each processed line so a transient PostLog
// failure never stalls delivery of subsequent entries.
func forwardNewLines(logPath string, ticketID string, forwarded *int, c *client.Client) {
	f, err := os.Open(logPath) //nolint:gosec
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }() // read-only: a close error cannot affect what was read

	scanner := bufio.NewScanner(f)
	line := 0
	for scanner.Scan() {
		if line >= *forwarded {
			raw := scanner.Text()
			var r rawBlogEntry
			if err := json.Unmarshal([]byte(raw), &r); err == nil {
				p := client.LogPayload{
					EntryType: r.Type,
					FromRole:  r.Role,
					Message:   r.Message,
				}
				if _, postErr := c.PostLog(ticketID, p); postErr != nil {
					log.Printf("executor: post log line %d: %v", line, postErr)
				}
			}
			*forwarded++
		}
		line++
	}
}

// readState reads .golem/tickets/<id>/state.json and returns (phase, sha, error).
func readState(ticketDir string) (string, string, error) {
	statePath := filepath.Join(ticketDir, "state.json")
	data, err := os.ReadFile(statePath) //nolint:gosec
	if err != nil {
		return "", "", err
	}

	var state struct {
		Phase string `json:"phase"`
		SHA   string `json:"sha"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", "", err
	}
	return state.Phase, state.SHA, nil
}

// pushCredentialEnv are the variables deploy/shem-entrypoint.sh's git
// credential helpers expand at use time. They are the one value in this
// process's environment that is worth stealing.
var pushCredentialEnv = []string{"GOLEM_PUSH_TOKEN", "GOLEM_GITHUB_TOKEN"}

// pushMirrorPath is where Golem keeps its own bare clone of a repository, as
// a sibling of the repository rather than a child of it. The agent works
// inside repoPath and is entitled to write anything there; the mirror has to
// be somewhere it is not.
func pushMirrorPath(repoPath string) string {
	repoPath = filepath.Clean(repoPath)
	return filepath.Join(filepath.Dir(repoPath), ".golem-push", filepath.Base(repoPath)+".git")
}

// withoutPushCredential returns env with the push token removed.
func withoutPushCredential(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		if !slices.Contains(pushCredentialEnv, name) {
			out = append(out, kv)
		}
	}
	return out
}

// ensureRemoteBaseRef creates createRef on the remote at sha (the pinned
// base) when it does not exist yet; an existing ref is never touched. Runs
// in the credential-carrying push mirror, like pushTicketBranch.
func ensureRemoteBaseRef(ctx context.Context, repoPath, remote, fetchRef, createRef, sha string) error {
	if remote == "" || createRef == "" || sha == "" || fetchRef == "" || fetchRef == createRef {
		return nil
	}
	mirror := pushMirrorPath(repoPath)
	if err := ensurePushMirror(ctx, mirror, remote); err != nil {
		return err
	}
	// The check-create below races siblings under git's ref locks rather
	// than failing on them, but it shares the mirror's refs/golem-base-fetch
	// scratch ref, so run the whole pass under the per-mirror lock.
	mu := forge.MirrorLock(mirror)
	mu.Lock()
	defer mu.Unlock()
	ls := exec.CommandContext(ctx, "git", "-C", mirror, "ls-remote", remote, "refs/heads/"+createRef)
	out, err := ls.CombinedOutput()
	if err != nil {
		return fmt.Errorf("checking remote for %s: %w\n%s", createRef, err, out)
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return nil // somebody already created it — nothing to do
	}
	fetch := exec.CommandContext(ctx, "git", "-C", mirror, "fetch", "--no-tags",
		remote, "+refs/heads/"+fetchRef+":refs/golem-base-fetch")
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("fetching %s to create %s: %w\n%s", fetchRef, createRef, err, out)
	}
	push := exec.CommandContext(ctx, "git", "-C", mirror, "push", remote, sha+":refs/heads/"+createRef)
	if out, err := push.CombinedOutput(); err != nil {
		// Children that finish together race this create: both check, both
		// push, one wins. The loser holds an error for the state it wanted
		// — the ref exists — so re-check before reporting a failure.
		verify := exec.CommandContext(ctx, "git", "-C", mirror, "ls-remote", remote, "refs/heads/"+createRef)
		if vout, verr := verify.CombinedOutput(); verr == nil && len(bytes.TrimSpace(vout)) > 0 {
			return nil
		}
		return fmt.Errorf("creating remote ref %s: %w\n%s", createRef, err, out)
	}
	return nil
}

// pushTicketBranch publishes the ticket branch to origin so the orchestrator
// can open a pull request against it. Golem has no other code path that
// pushes; agents remain denied `git push` by the tool-call gating policy.
//
// The push does NOT run in the agent's worktree, and this is the whole point
// of the function's shape.
//
// It used to: `git push` with cmd.Dir set to the worktree, inheriting this
// process's entire environment so the credential helper could expand
// GOLEM_GITHUB_TOKEN. Git reads configuration and runs hooks from the
// repository it is invoked in, and every one of those inputs is something
// the agent is entitled to write:
//
//   - core.hooksPath can point at a directory inside the working tree. That
//     is exactly what husky does, and the repository Golem is deployed
//     against has core.hooksPath = .husky/_, so a committed, ordinary file
//     would have executed with the token in its environment. A husky
//     pre-push hook has already run on this path in production — it failed
//     with a shell syntax error, which is how we know hooks execute here.
//   - a local credential.helper is consulted on the `approve` that follows a
//     successful push, which hands it the credential.
//   - url.<base>.insteadOf and remote.origin.pushurl redirect where the push
//     goes.
//
// So the branch is moved in two steps, and the token exists in only one of
// them:
//
//  1. A fetch INTO a Golem-owned bare mirror, FROM the worktree, with the
//     token stripped from the environment. The agent's repository is still
//     serving this fetch and can still run code through it
//     (uploadpack.packObjectsHook), but there is no longer a credential in
//     the environment for that code to take.
//  2. A push from the mirror to the real remote, with the token. The mirror
//     is created by Golem, has no hooks, and the agent never writes to it.
//
// remote is the repository URL from the orchestrator's claim, not the
// worktree's origin, so redirecting the push by editing the agent-side
// remote does not work either.
func pushTicketBranch(ctx context.Context, repoPath, worktreePath, branch, remote string) error {
	mirror := pushMirrorPath(repoPath)
	if err := ensurePushMirror(ctx, mirror, remote); err != nil {
		return err
	}
	// Every child for this repository publishes through the same bare mirror.
	// Serialize both git operations: fetch writes objects and refs into the
	// mirror, and pushing must not race an integration merge or another branch
	// fetch in that same object store.
	mu := forge.MirrorLock(mirror)
	mu.Lock()
	defer mu.Unlock()

	// Step 1 — token-free. Anything the agent's repo can make git run during
	// this fetch runs without the credential.
	fetch := exec.CommandContext(ctx, "git", "-C", mirror, "fetch", "--no-tags",
		worktreePath, "+"+branch+":"+branch)
	fetch.Env = withoutPushCredential(os.Environ())
	if out, err := fetch.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch %s into push mirror: %w\n%s", branch, err, out)
	}

	// Step 2 — carries the credential, in a repository only Golem writes.
	push := exec.CommandContext(ctx, "git", "-C", mirror, "push", remote,
		"+"+branch+":"+branch)
	out, err := push.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push %s: %w\n%s", branch, err, out)
	}
	return nil
}

// ensurePushMirror creates the bare mirror if it is not there yet. It is
// deliberately a plain bare repository with no hooks and no remote of its
// own: the push names its remote explicitly, so nothing about where this
// pushes can be changed by editing config on disk.
func ensurePushMirror(ctx context.Context, mirror, remote string) error {
	if remote == "" {
		return fmt.Errorf("push mirror: no remote for this repository")
	}
	// git init --bare is not safe to run twice on the same directory at
	// once, and the forge merge lane shares this mirror, so create it under
	// the forge's per-mirror lock.
	mu := forge.MirrorLock(mirror)
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o750); err != nil {
		return fmt.Errorf("push mirror: %w", err)
	}
	init := exec.CommandContext(ctx, "git", "init", "--bare", "--template=", mirror)
	init.Env = withoutPushCredential(os.Environ())
	if out, err := init.CombinedOutput(); err != nil {
		return fmt.Errorf("push mirror: git init --bare: %w\n%s", err, out)
	}
	return nil
}

// setPushURL sets (or clears) the git remote.origin.pushurl in the repo at path.
func setPushURL(repoPath, url string) {
	var args []string
	if url == "" {
		args = []string{"-C", repoPath, "config", "--local", "--unset", "remote.origin.pushurl"}
	} else {
		args = []string{"-C", repoPath, "config", "--local", "remote.origin.pushurl", url}
	}
	_ = asAgent(exec.Command("git", args...)).Run() //nolint:gosec
}

// repoLocalPath returns the local filesystem path for the given normalized remote URL.
func repoLocalPath(cfg *config.Config, normalizedRemote string) string {
	for _, r := range cfg.Repos {
		if r.NormalizedRemote == normalizedRemote {
			return r.Path
		}
	}
	return ""
}

// repoCloneRemote returns the URL to clone from: the one the operator wrote in
// shem.yaml, not the normalized form, which has had ".git" and its case
// stripped and is only meant to identify a repository. Falls back to the given
// remote when no repo is configured for it.
func repoCloneRemote(cfg *config.Config, normalizedRemote string) string {
	for _, r := range cfg.Repos {
		if r.NormalizedRemote == normalizedRemote && r.Remote != "" {
			return r.Remote
		}
	}
	return normalizedRemote
}

// firstLineOf trims a multi-line command failure to something that reads as a
// status line. The full text goes to the shem log.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// generatePRDescription asks the pr-description role for a pull request body,
// written from this branch's diff and the ticket's own log.
//
// Returns an empty body and an error rather than a fabricated one when the
// role cannot run: the orchestrator's fallback is a minimal but honest body,
// which is better than a confident description of work nobody described.
// The issue number is deliberately not passed: the claim does not carry one,
// and the orchestrator appends the closing reference itself from the ticket
// row — the one place that actually knows it.
func (e *GolemExecutor) generatePRDescription(ctx context.Context, repoPath, ticketID, model string) (string, error) {
	args := e.Agent.subcommandArgs([]string{"ticket", "pr-description", "--ticket", ticketID}, model)
	cmd := asGolemCLI(exec.CommandContext(ctx, "golem", args...))
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
