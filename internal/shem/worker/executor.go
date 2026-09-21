package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"gopkg.in/yaml.v3"
)

// GolemExecutor runs tickets phase by phase with human approval gates.
type GolemExecutor struct {
	repoMu sync.Map // keyed by repoPath, value *sync.Mutex
}

// postStatus sends a STATUS log entry to the orchestrator and prints locally.
// Errors posting to the orchestrator are logged and ignored — best-effort.
func postStatus(c *client.Client, ticketID, msg string) {
	log.Printf("executor [%s]: %s", ticketID[:8], msg)
	if _, err := c.PostLog(ticketID, client.LogPayload{
		EntryType: "STATUS",
		FromRole:  "shem",
		Message:   msg,
	}); err != nil {
		log.Printf("executor [%s]: postStatus error: %v", ticketID[:8], err)
	}
}

func (e *GolemExecutor) repoMutex(repoPath string) *sync.Mutex {
	v, _ := e.repoMu.LoadOrStore(repoPath, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// initRepo acquires the per-repo mutex, runs ensureRepoReady, and releases the
// mutex before returning. Using a helper keeps the defer scope tight.
func (e *GolemExecutor) initRepo(ctx context.Context, repoPath string) error {
	mu := e.repoMutex(repoPath)
	mu.Lock()
	defer mu.Unlock()
	return ensureRepoReady(ctx, repoPath)
}

// RunTicket executes a claimed ticket:
//  1. Pre-creates the local ticket with `golem ticket new` (scaffolding only, no Claude).
//  2. Ensures the repo is initialized and the code graph is current.
//  3. Runs three separate `claude --print` sessions — brainstorm, plan, implement —
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
	if err := CloneIfMissing(ctx, repoPath, claim.RepoRemote); err != nil {
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
	trustWorkspace(repoPath)

	if err := e.initRepo(ctx, repoPath); err != nil {
		log.Printf("executor: repo pre-flight warning: %v", err)
	}

	ticketID := claim.TicketID
	ticketDir := filepath.Join(repoPath, ".golem", "tickets", ticketID)
	logPath := filepath.Join(ticketDir, "log.jsonl")

	if cfg.NoPush {
		setPushURL(repoPath, repoPath)
		defer setPushURL(repoPath, "")
	}

	// Determine starting phase and ensure the ticket exists locally.
	startPhase := "brainstorm"
	if claim.CheckpointPhase != nil {
		postStatus(c, ticketID, "Resuming from checkpoint: "+*claim.CheckpointPhase)
		if err := RecoverTicket(ctx, claim, cfg); err != nil {
			return fmt.Errorf("recovery failed: %w", err)
		}
		startPhase = *claim.CheckpointPhase
		// A checkpoint records a phase that both COMPLETED and PASSED its
		// validation — brainstorm and plan are checkpointed only after
		// runGolemValidate returns — so there is nothing left to wait for and
		// the next phase always starts.
		//
		// This used to ask GetPendingApproval whether a human had approved
		// yet, and stayed put if not. There is no approval to pend on now:
		// intake is the only human gate, and it is passed before the ticket is
		// ever claimable.
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
			if err := runGolemTicketNew(ctx, repoPath, ticketID, claim.Branch, claim.Description); err != nil {
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

	// Start log-tail before any Claude invocations so we capture all entries.
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

	// Phase loop: brainstorm → (human approval) → plan → (human approval) → implement.
	for phase := startPhase; ; {
		switch phase {
		case "brainstorm":
			// No human approval gate here any more. Intake is the only
			// human step: a person reads the description and starts the
			// ticket. Everything after that advances on the shem's own
			// validation, so the validation is the gate and a failure
			// parks the ticket in needs-attention rather than waiting.
			feedback := consumeFeedback(ctx, c, claim.TicketID)
			postStatus(c, ticketID, "Starting agent (claude) — brainstorm phase")
			prompt := buildBrainstormPrompt(ticketID, claim.Description, feedback)
			if err := runClaudePhase(ctx, repoPath, prompt, filepath.Join(ticketDir, "claude-brainstorm.log")); err != nil {
				return err
			}
			postDocumentEntry(c, claim.TicketID, "SPEC", filepath.Join(ticketDir, "spec.md"))
			postStatus(c, ticketID, "Brainstorm complete — validating the spec")
			if err := runGolemValidate(ctx, repoPath, ticketID, "spec"); err != nil {
				return fmt.Errorf("spec validation: %w", err)
			}
			postStatus(c, ticketID, "Spec validated — advancing to planning")
			// Checkpointed only after validation passes, so a checkpoint at
			// "brainstorm" means the stage is finished AND approved. That is
			// what lets a resume advance straight to the next phase without
			// having to ask whether an approval is still outstanding.
			PostCheckpointWithRetry(c, claim.TicketID, "brainstorm", "", 5) //nolint:errcheck
			if err := runGolemAdvance(ctx, repoPath, ticketID, "plan"); err != nil {
				log.Printf("executor: advance to plan: %v", err)
			}
			if phErr := c.PostPhase(claim.TicketID, "plan"); phErr != nil {
				if errors.Is(phErr, client.ErrNotOwner) {
					log.Printf("executor: ticket %s was requeued, stopping", claim.TicketID)
					return nil
				}
				log.Printf("executor: post phase error: %v", phErr)
			}
			phase = "plan"

		case "plan":
			feedback := consumeFeedback(ctx, c, claim.TicketID)
			postStatus(c, ticketID, "Starting agent (claude) — planning phase")
			prompt := buildPlanPrompt(ticketID, claim.Description, feedback)
			if err := runClaudePhase(ctx, repoPath, prompt, filepath.Join(ticketDir, "claude-plan.log")); err != nil {
				return err
			}
			postDocumentEntry(c, claim.TicketID, "PLAN", filepath.Join(ticketDir, "plan.md"))
			postStatus(c, ticketID, "Plan complete — validating the plan")
			if err := runGolemValidate(ctx, repoPath, ticketID, "plan"); err != nil {
				return fmt.Errorf("plan validation: %w", err)
			}
			postStatus(c, ticketID, "Plan validated — starting implementation")
			PostCheckpointWithRetry(c, claim.TicketID, "plan", "", 5) //nolint:errcheck
			if err := runGolemAdvance(ctx, repoPath, ticketID, "implement"); err != nil {
				log.Printf("executor: advance to implement: %v", err)
			}
			if phErr := c.PostPhase(claim.TicketID, "implement"); phErr != nil {
				if errors.Is(phErr, client.ErrNotOwner) {
					log.Printf("executor: ticket %s was requeued, stopping", claim.TicketID)
					return nil
				}
				log.Printf("executor: post phase error: %v", phErr)
			}
			phase = "implement"

		case "implement":
			postStatus(c, ticketID, "Starting agent (claude) — implementation phase")
			if err := runClaudePhase(ctx, repoPath, buildImplementPrompt(ticketID, claim.Branch, claim.Description), filepath.Join(ticketDir, "claude-implement.log")); err != nil {
				return err
			}
			if revErr := runGolemReview(ctx, repoPath, ticketID); revErr != nil {
				postStatus(c, ticketID, "Review gate failed to run: "+firstLineOf(revErr.Error()))
				log.Printf("executor: review gate for %s: %v", ticketID, revErr)
			}
			return finishWorkPhase(ctx, cfg, c, claim, repoPath, ticketDir, "Implementation", claim.Branch)

		case "revising":
			feedback := consumeFeedback(ctx, c, claim.TicketID)
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
			postStatus(c, ticketID, "Starting agent (claude) — revision phase")
			if err := runClaudePhase(ctx, repoPath, buildRevisePrompt(ticketID, claim.Branch, claim.Description, feedback), filepath.Join(ticketDir, "claude-revise.log")); err != nil {
				return err
			}
			if revErr := runGolemReview(ctx, repoPath, ticketID); revErr != nil {
				postStatus(c, ticketID, "Review gate failed to run: "+firstLineOf(revErr.Error()))
				log.Printf("executor: review gate for %s: %v", ticketID, revErr)
			}
			// Previously defaulted an unreadable state to "ready-for-review",
			// so a revise run that left no state reported success outright.
			return finishWorkPhase(ctx, cfg, c, claim, repoPath, ticketDir, "Revision", claim.Branch)

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
func golemTicketNewArgs(ticketID, branch, description string) []string {
	return []string{"ticket", "new", "--ticket-id", ticketID, "--branch", branch, "--", description}
}

// runGolemTicketNew creates the local ticket scaffold (worktree + branch) without
// invoking Claude. Claude's role starts at brainstorm, after the scaffold exists.
func runGolemTicketNew(ctx context.Context, repoPath, ticketID, branch, description string) error {
	cmd := asAgent(exec.CommandContext(ctx, "golem", golemTicketNewArgs(ticketID, branch, description)...))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	log.Printf("executor: created ticket %s in %s", ticketID, repoPath)
	return nil
}

// runGolemAdvance advances the local ticket to the given phase.
func runGolemAdvance(ctx context.Context, repoPath, ticketID, toPhase string) error {
	cmd := asAgent(exec.CommandContext(ctx, "golem", "ticket", "advance", "--ticket", ticketID, "--to", toPhase))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// runGolemReview runs the review gate — the reviewer agent, then the
// configured gate commands — and is what advances the local ticket to
// ready-for-review.
//
// Run by the SHEM, not by the agent, for two reasons.
//
// It could not work from the agent. Claude Code runs the agent's shell
// commands in a sandbox that does not expose the environment, so `golem`
// started from there has no model credential, and the nested `claude` the
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
func runGolemValidate(ctx context.Context, repoPath, ticketID, stage string) error {
	cmd := asAgent(exec.CommandContext(ctx, "golem", "ticket", "validate",
		"--ticket", ticketID, "--stage", stage))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func runGolemReview(ctx context.Context, repoPath, ticketID string) error {
	cmd := asAgent(exec.CommandContext(ctx, "golem", "ticket", "review", "--ticket", ticketID))
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// claudePhaseCmd builds the agent subprocess. It is separate from
// runClaudePhase only so a test can inspect what is handed to the agent
// without executing it — see TestClaudePhaseCmdDoesNotLeakGolemSecrets.
func claudePhaseCmd(ctx context.Context, repoPath, prompt string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "claude", "--print")
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// The prompt carries untrusted issue text, so this process must not
	// carry golem's credentials — see internal/agentenv and asAgent.
	cmd.Env = agentenv.Environ()
	// Drop to an unprivileged account where one is configured. Environment
	// filtering keeps the token out of the agent's OWN environment, but an
	// agent running as root simply reads it out of /proc/1/environ instead —
	// verified in the deployed container. Filtering is only meaningful once
	// the agent cannot read the shem's memory.
	agentenv.DropPrivileges(cmd)
	return cmd
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

// runClaudePhase runs a single `claude --print` session with the given prompt.
// Output is written to os.Stdout and also teed to logPath for post-mortem inspection.
func runClaudePhase(ctx context.Context, repoPath, prompt, logPath string) error {
	cmd := claudePhaseCmd(ctx, repoPath, prompt)

	f, err := os.Create(logPath) //nolint:gosec
	if err != nil {
		log.Printf("executor: could not create phase log %s: %v", logPath, err)
	} else {
		defer func() {
			if cerr := f.Close(); cerr != nil {
				log.Printf("executor: closing phase log %s: %v", logPath, cerr)
			}
		}()
		cmd.Stdout = io.MultiWriter(os.Stdout, f)
		cmd.Stderr = io.MultiWriter(os.Stderr, f)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude --print: %w", err)
	}
	return nil
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

// toOrchestratorPhase maps the local golem state.json phase to the orchestrator
// phase name. "review" and "closed" both become "ready-for-review" because humans
// close tickets via the UI — the shem never auto-closes.
// finishWorkPhase closes out an implement or revise run: it works out what the
// agent actually left behind, reports that, and only then moves the ticket.
//
// The order matters and used to be wrong. Both branches posted
// "… complete — ready for review" BEFORE reading the local state, then derived
// the orchestrator phase from that state and posted it — and
// toOrchestratorPhase passes anything that is not "review" or "closed"
// straight through. So an agent that stopped without running
// `golem ticket review` left state.json at, say, "plan", and the shem posted
// phase "plan": the ticket moved BACKWARDS while the log above it claimed it
// was ready for review.
//
// That combination also looped. "plan" is in the resumable set, so every shem
// restart resumed the ticket, nextPhaseAfterCheckpoint sent it to implement
// again, and the whole pass re-ran and re-appended its entries indefinitely.
//
// An agent that did not advance the local ticket has not finished, whatever
// the reason — a question for a human, a refusal, a crash after the last
// commit. That is for a human to look at, so this returns an error and lets
// the worker park the ticket in needs-attention with the reason attached,
// rather than inventing a phase for it.
func finishWorkPhase(ctx context.Context, cfg *config.Config, c *client.Client,
	claim *client.ClaimResponse, repoPath, ticketDir, what, branch string,
) error {
	ticketID := claim.TicketID

	localPhase, sha, stateErr := readState(ticketDir)
	orchPhase := toOrchestratorPhase(localPhase)

	// Written either way, and before the early return: the checkpoint is what
	// lets a requeue resume this phase instead of starting the ticket over
	// from brainstorm.
	if sha != "" {
		PostCheckpointWithRetry(c, ticketID, localPhase, sha, 5) //nolint:errcheck
	}

	if orchPhase != "ready-for-review" {
		// A failed gate is a real verdict, not a missing step. `golem ticket
		// review` writes needs-attention itself when the gate commands do not
		// pass, and reporting that as "did not complete" would blame the
		// agent for work the gate deliberately rejected.
		if localPhase == "needs-attention" {
			return fmt.Errorf("%s finished but the review gate did not pass — "+
				"see the reviewer attestation in the log above", what)
		}
		// The local phase is named because it is the whole diagnosis: "plan"
		// means the agent stopped at the planning gate, "implement" means the
		// review gate did not run or did not reach a verdict.
		if stateErr != nil {
			return fmt.Errorf("%s did not complete: no readable ticket state in %s (%v), "+
				"so there is nothing to review", what, ticketDir, stateErr)
		}
		return fmt.Errorf("%s did not complete: the agent left the local ticket at phase %q "+
			"instead of advancing it, so it is not ready for review — see the log above for "+
			"what it was waiting on", what, localPhase)
	}

	postStatus(c, ticketID, what+" complete — ready for review")

	if !cfg.NoPush {
		worktree := filepath.Join(ticketDir, "worktree")
		if pushErr := pushTicketBranch(ctx, repoPath, worktree, branch, claim.RepoRemote); pushErr != nil {
			// A failed push must not block the lifecycle: the ticket still
			// reaches ready-for-review, just without a pull request.
			postStatus(c, ticketID, "Branch push failed, no pull request will be opened: "+pushErr.Error())
		} else {
			postStatus(c, ticketID, "Pushed "+branch+" to origin")
			// Generated here, after the push and before the report, because
			// the description is written from the branch's own diff and only
			// this host has it. A failure is logged and the report goes out
			// anyway: the orchestrator falls back to a minimal body, so a
			// missing description costs a good write-up, not the pull
			// request.
			prBody, bodyErr := generatePRDescription(ctx, repoPath, ticketID)
			if bodyErr != nil {
				postStatus(c, ticketID, "Could not generate the pull request description: "+
					firstLineOf(bodyErr.Error()))
				log.Printf("executor: pr description for %s: %v", ticketID, bodyErr)
			}
			if pErr := c.PostBranchPushed(ticketID, prBody); pErr != nil {
				log.Printf("executor: post branch-pushed: %v", pErr)
			}
		}
	}

	if phErr := c.PostPhase(ticketID, orchPhase); phErr != nil {
		if errors.Is(phErr, client.ErrNotOwner) {
			log.Printf("executor: ticket %s was requeued, stopping", ticketID)
			return nil
		}
		log.Printf("executor: post phase error: %v", phErr)
	}
	return nil
}

func toOrchestratorPhase(localPhase string) string {
	switch localPhase {
	case "review", "closed":
		return "ready-for-review"
	default:
		return localPhase
	}
}

// nextPhaseAfterCheckpoint returns the phase to execute after a completed
// checkpoint phase. Brainstorm and plan each have a human approval gate; once
// that gate is passed the checkpoint is set and the next run should skip ahead.
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
func ensureRepoReady(ctx context.Context, repoPath string) error {
	configPath := filepath.Join(repoPath, ".golem", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		out, err := asAgent(exec.CommandContext(ctx, "golem", "init", "--backend", "claude-code", "--repo", repoPath)).CombinedOutput()
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
		out, err := asAgent(exec.CommandContext(ctx, "golem", "graph", "build", "--repo", repoPath)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("golem graph build: %w\n%s", err, out)
		}
		log.Printf("executor: built graph in %s", repoPath)
	} else {
		out, err := asAgent(exec.CommandContext(ctx, "golem", "graph", "update", "--repo", repoPath)).CombinedOutput()
		if err != nil {
			log.Printf("executor: graph update failed, rebuilding: %v\n%s", err, out)
			out, err = asAgent(exec.CommandContext(ctx, "golem", "graph", "build", "--repo", repoPath)).CombinedOutput()
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

// pushCredentialEnv is the variable deploy/shem-entrypoint.sh's git
// credential helper expands at use time. It is the one value in this
// process's environment that is worth stealing.
const pushCredentialEnv = "GOLEM_GITHUB_TOKEN"

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
		if name, _, ok := strings.Cut(kv, "="); ok && name == pushCredentialEnv {
			continue
		}
		out = append(out, kv)
	}
	return out
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
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(mirror), 0o750); err != nil {
		return fmt.Errorf("push mirror: %w", err)
	}
	init := exec.CommandContext(ctx, "git", "init", "--bare", mirror)
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
func generatePRDescription(ctx context.Context, repoPath, ticketID string) (string, error) {
	cmd := asAgent(exec.CommandContext(ctx, "golem", "ticket", "pr-description", "--ticket", ticketID))
	cmd.Dir = repoPath
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}
