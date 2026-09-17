package worker

import (
	"bufio"
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
		// Checkpoint records the last *completed* phase. If the approval gate for
		// that phase was already passed (no pending approval), advance to the next
		// phase. If approval is still pending, stay so the wait loop re-enters.
		if startPhase == "brainstorm" || startPhase == "plan" {
			if pending, _ := c.GetPendingApproval(claim.TicketID); pending == nil {
				startPhase = nextPhaseAfterCheckpoint(startPhase)
				postStatus(c, ticketID, "Checkpoint approved — advancing to "+startPhase)
			} else {
				postStatus(c, ticketID, "Approval still pending — waiting for human")
			}
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
			for {
				// Skip re-running if approval is already pending (shem restart mid-wait).
				pending, _ := c.GetPendingApproval(claim.TicketID)
				if pending == nil {
					feedback := consumeFeedback(ctx, c, claim.TicketID)
					postStatus(c, ticketID, "Starting agent (claude) — brainstorm phase")
					prompt := buildBrainstormPrompt(ticketID, claim.Description, feedback)
					if err := runClaudePhase(ctx, repoPath, prompt, filepath.Join(ticketDir, "claude-brainstorm.log")); err != nil {
						return err
					}
					postStatus(c, ticketID, "Brainstorm complete — spec ready for review")
					PostCheckpointWithRetry(c, claim.TicketID, "brainstorm", "", 5) //nolint:errcheck
					postDocumentEntry(c, claim.TicketID, "SPEC", filepath.Join(ticketDir, "spec.md"))
					if err := c.PostApprovalRequest(claim.TicketID, "Brainstorm complete. Review the spec and approve to continue to planning."); err != nil {
						log.Printf("executor: post approval request: %v", err)
					}
				}
				postStatus(c, ticketID, "Waiting for spec approval…")
				if err := waitForApproval(ctx, c, claim.TicketID); err != nil {
					return fmt.Errorf("brainstorm approval: %w", err)
				}
				// If the human requested changes, loop and re-run with their feedback.
				if fb, _ := c.GetPendingFeedback(claim.TicketID); fb != nil {
					postStatus(c, ticketID, "Spec changes requested — re-running brainstorm")
					continue
				}
				postStatus(c, ticketID, "Spec approved — advancing to planning")
				break
			}
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
			for {
				pending, _ := c.GetPendingApproval(claim.TicketID)
				if pending == nil {
					feedback := consumeFeedback(ctx, c, claim.TicketID)
					postStatus(c, ticketID, "Starting agent (claude) — planning phase")
					prompt := buildPlanPrompt(ticketID, claim.Description, feedback)
					if err := runClaudePhase(ctx, repoPath, prompt, filepath.Join(ticketDir, "claude-plan.log")); err != nil {
						return err
					}
					postStatus(c, ticketID, "Plan complete — ready for review")
					PostCheckpointWithRetry(c, claim.TicketID, "plan", "", 5) //nolint:errcheck
					postDocumentEntry(c, claim.TicketID, "PLAN", filepath.Join(ticketDir, "plan.md"))
					if err := c.PostApprovalRequest(claim.TicketID, "Plan complete. Review the plan and approve to begin implementation."); err != nil {
						log.Printf("executor: post approval request: %v", err)
					}
				}
				postStatus(c, ticketID, "Waiting for plan approval…")
				if err := waitForApproval(ctx, c, claim.TicketID); err != nil {
					return fmt.Errorf("plan approval: %w", err)
				}
				if fb, _ := c.GetPendingFeedback(claim.TicketID); fb != nil {
					postStatus(c, ticketID, "Plan changes requested — re-running planning")
					continue
				}
				postStatus(c, ticketID, "Plan approved — starting implementation")
				break
			}
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
			if err := runClaudePhase(ctx, repoPath, buildImplementPrompt(ticketID, claim.Description), filepath.Join(ticketDir, "claude-implement.log")); err != nil {
				return err
			}
			postStatus(c, ticketID, "Implementation complete — ready for review")
			finalPhase, sha, _ := readState(ticketDir)
			if finalPhase == "" {
				finalPhase = "implement"
			}
			orchPhase := toOrchestratorPhase(finalPhase)
			if orchPhase == "ready-for-review" && !cfg.NoPush {
				worktree := filepath.Join(ticketDir, "worktree")
				if pushErr := pushTicketBranch(ctx, worktree, claim.Branch); pushErr != nil {
					// A failed push must not block the lifecycle: the ticket
					// still reaches ready-for-review, just without a PR.
					postStatus(c, ticketID, "Branch push failed, no pull request will be opened: "+pushErr.Error())
				} else {
					postStatus(c, ticketID, "Pushed "+claim.Branch+" to origin")
					if pErr := c.PostBranchPushed(claim.TicketID); pErr != nil {
						log.Printf("executor: post branch-pushed: %v", pErr)
					}
				}
			}
			if phErr := c.PostPhase(claim.TicketID, orchPhase); phErr != nil {
				if errors.Is(phErr, client.ErrNotOwner) {
					log.Printf("executor: ticket %s was requeued, stopping", claim.TicketID)
					return nil
				}
				log.Printf("executor: post phase error: %v", phErr)
			}
			if sha != "" {
				PostCheckpointWithRetry(c, claim.TicketID, finalPhase, sha, 5) //nolint:errcheck
			}
			return nil

		case "revising":
			feedback := consumeFeedback(ctx, c, claim.TicketID)
			postStatus(c, ticketID, "Starting agent (claude) — revision phase")
			if err := runClaudePhase(ctx, repoPath, buildRevisePrompt(ticketID, claim.Description, feedback), filepath.Join(ticketDir, "claude-revise.log")); err != nil {
				return err
			}
			postStatus(c, ticketID, "Revision complete — ready for review")
			finalPhase, sha, _ := readState(ticketDir)
			if finalPhase == "" {
				finalPhase = "ready-for-review"
			}
			orchPhase := toOrchestratorPhase(finalPhase)
			if orchPhase == "ready-for-review" && !cfg.NoPush {
				worktree := filepath.Join(ticketDir, "worktree")
				if pushErr := pushTicketBranch(ctx, worktree, claim.Branch); pushErr != nil {
					// A failed push must not block the lifecycle: the ticket
					// still reaches ready-for-review, just without a PR.
					postStatus(c, ticketID, "Branch push failed, no pull request will be opened: "+pushErr.Error())
				} else {
					postStatus(c, ticketID, "Pushed "+claim.Branch+" to origin")
					if pErr := c.PostBranchPushed(claim.TicketID); pErr != nil {
						log.Printf("executor: post branch-pushed: %v", pErr)
					}
				}
			}
			if phErr := c.PostPhase(claim.TicketID, orchPhase); phErr != nil {
				if errors.Is(phErr, client.ErrNotOwner) {
					log.Printf("executor: ticket %s was requeued, stopping", claim.TicketID)
					return nil
				}
				log.Printf("executor: post phase error: %v", phErr)
			}
			if sha != "" {
				PostCheckpointWithRetry(c, claim.TicketID, finalPhase, sha, 5) //nolint:errcheck
			}
			return nil

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
	cmd := exec.CommandContext(ctx, "golem", golemTicketNewArgs(ticketID, branch, description)...)
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
	cmd := exec.CommandContext(ctx, "golem", "ticket", "advance", "--ticket", ticketID, "--to", toPhase)
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
	// carry golem's credentials — see internal/agentenv. The golem and git
	// subprocesses around it are golem's own commands and keep the full
	// environment, which is what leaves the shem's push credential working.
	cmd.Env = agentenv.Environ()
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

// waitForApproval polls until the pending approval HumanInput for ticketID is
// resolved (GetPendingApproval returns nil). The human resolves it by clicking
// "Approve" in the orchestrator UI.  Returns only on context cancellation or
// when approval is confirmed.
func waitForApproval(ctx context.Context, c *client.Client, ticketID string) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			pending, err := c.GetPendingApproval(ticketID)
			if err != nil {
				log.Printf("executor: polling approval for ticket %s: %v", ticketID, err)
				continue
			}
			if pending == nil {
				return nil
			}
		}
	}
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
		out, err := exec.CommandContext(ctx, "golem", "init", "--backend", "claude-code", "--repo", repoPath).CombinedOutput()
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
		out, err := exec.CommandContext(ctx, "golem", "graph", "build", "--repo", repoPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("golem graph build: %w\n%s", err, out)
		}
		log.Printf("executor: built graph in %s", repoPath)
	} else {
		out, err := exec.CommandContext(ctx, "golem", "graph", "update", "--repo", repoPath).CombinedOutput()
		if err != nil {
			log.Printf("executor: graph update failed, rebuilding: %v\n%s", err, out)
			out, err = exec.CommandContext(ctx, "golem", "graph", "build", "--repo", repoPath).CombinedOutput()
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

// pushTicketBranch publishes the ticket branch to origin so the orchestrator
// can open a pull request against it. Golem has no other code path that
// pushes; agents remain denied `git push` by the tool-call gating policy.
//
// This is one of Golem's own subprocesses and deliberately inherits the whole
// environment, unlike the agent (see agentenv.go). That is what leaves
// deploy/shem-entrypoint.sh's credential helper working: the helper expands
// GOLEM_GITHUB_TOKEN at use time, so this push gets the token and a `git
// credential fill` run by the agent gets an empty password.
func pushTicketBranch(ctx context.Context, worktreePath, branch string) error {
	cmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", branch)
	cmd.Dir = worktreePath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git push %s: %w\n%s", branch, err, out)
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
	_ = exec.Command("git", args...).Run() //nolint:gosec
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
