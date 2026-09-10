package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// GolemExecutor runs tickets phase by phase with human approval gates.
type GolemExecutor struct {
	repoMu sync.Map // keyed by repoPath, value *sync.Mutex
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
		if err := RecoverTicket(ctx, claim, cfg); err != nil {
			return fmt.Errorf("recovery failed: %w", err)
		}
		startPhase = *claim.CheckpointPhase
	} else {
		// Fresh orchestrator ticket (no checkpoint). Always start from brainstorm.
		// If the ticket directory already exists (e.g. after a requeue) reuse the
		// existing worktree instead of trying to create it again.
		if _, statErr := os.Stat(ticketDir); os.IsNotExist(statErr) {
			if err := runGolemTicketNew(ctx, repoPath, ticketID, claim.Description); err != nil {
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
					prompt := buildBrainstormPrompt(ticketID, claim.Description, feedback)
					if err := runClaudePhase(ctx, repoPath, prompt); err != nil {
						return err
					}
					PostCheckpointWithRetry(c, claim.TicketID, "brainstorm", "", 5) //nolint:errcheck
					postDocumentEntry(c, claim.TicketID, "SPEC", filepath.Join(ticketDir, "spec.md"))
					if err := c.PostApprovalRequest(claim.TicketID, "Brainstorm complete. Review the spec and approve to continue to planning."); err != nil {
						log.Printf("executor: post approval request: %v", err)
					}
				}
				log.Printf("executor: ticket %s waiting for brainstorm approval", claim.TicketID)
				if err := waitForApproval(ctx, c, claim.TicketID); err != nil {
					return fmt.Errorf("brainstorm approval: %w", err)
				}
				// If the human requested changes, loop and re-run with their feedback.
				if fb, _ := c.GetPendingFeedback(claim.TicketID); fb != nil {
					log.Printf("executor: ticket %s brainstorm changes requested, re-running", claim.TicketID)
					continue
				}
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
					prompt := buildPlanPrompt(ticketID, claim.Description, feedback)
					if err := runClaudePhase(ctx, repoPath, prompt); err != nil {
						return err
					}
					PostCheckpointWithRetry(c, claim.TicketID, "plan", "", 5) //nolint:errcheck
					postDocumentEntry(c, claim.TicketID, "PLAN", filepath.Join(ticketDir, "plan.md"))
					if err := c.PostApprovalRequest(claim.TicketID, "Plan complete. Review the plan and approve to begin implementation."); err != nil {
						log.Printf("executor: post approval request: %v", err)
					}
				}
				log.Printf("executor: ticket %s waiting for plan approval", claim.TicketID)
				if err := waitForApproval(ctx, c, claim.TicketID); err != nil {
					return fmt.Errorf("plan approval: %w", err)
				}
				if fb, _ := c.GetPendingFeedback(claim.TicketID); fb != nil {
					log.Printf("executor: ticket %s plan changes requested, re-running", claim.TicketID)
					continue
				}
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
			if err := runClaudePhase(ctx, repoPath, buildImplementPrompt(ticketID, claim.Description)); err != nil {
				return err
			}
			finalPhase, sha, _ := readState(ticketDir)
			if finalPhase == "" {
				finalPhase = "implement"
			}
			orchPhase := toOrchestratorPhase(finalPhase)
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
			if err := runClaudePhase(ctx, repoPath, buildRevisePrompt(ticketID, claim.Description, feedback)); err != nil {
				return err
			}
			finalPhase, sha, _ := readState(ticketDir)
			if finalPhase == "" {
				finalPhase = "ready-for-review"
			}
			orchPhase := toOrchestratorPhase(finalPhase)
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

// runGolemTicketNew creates the local ticket scaffold (worktree + branch) without
// invoking Claude. Claude's role starts at brainstorm, after the scaffold exists.
func runGolemTicketNew(ctx context.Context, repoPath, ticketID, description string) error {
	cmd := exec.CommandContext(ctx, "golem", "ticket", "new", "--ticket-id", ticketID, description)
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

// runClaudePhase runs a single `claude --print` session with the given prompt.
func runClaudePhase(ctx context.Context, repoPath, prompt string) error {
	cmd := exec.CommandContext(ctx, "claude", "--print")
	cmd.Dir = repoPath
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
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

// buildBrainstormPrompt returns the prompt for the brainstorm Claude session.
// Claude writes a spec and stops — it does NOT advance the phase.
// If feedback is non-empty the spec must address that feedback.
func buildBrainstormPrompt(ticketID, description, feedback string) string {
	feedbackSection := ""
	if feedback != "" {
		feedbackSection = fmt.Sprintf(`
HUMAN FEEDBACK ON PREVIOUS SPEC (you MUST address all points):
%s

`, feedback)
	}
	return fmt.Sprintf(`You are a Golem developer running autonomously.
The ticket has been created. Complete the BRAINSTORM phase only.

Ticket ID: %s
Description: %s
Ticket directory: .golem/tickets/%s/
%s
Instructions:
1. Read .golem/roles/spec-adherence.md for spec guidelines.
2. Write a clear spec to .golem/tickets/%s/spec.md.
   Use markdown. Cover: goal, scope (what is and isn't included),
   key design decisions, data model / API shape if relevant, and
   acceptance criteria. Be specific enough that an implementer has
   no ambiguity.
3. Log the completion:
   golem log emit --ticket %s --role developer --type STATUS "Brainstorm complete: <one-line summary of spec>"

STOP after step 3. Do NOT run golem ticket advance or any other ticket lifecycle commands.
A human will review your spec in the orchestrator UI and approve before planning begins.`,
		ticketID, description, ticketID, feedbackSection, ticketID, ticketID)
}

// buildPlanPrompt returns the prompt for the plan Claude session.
// Claude writes an implementation plan and stops — it does NOT advance the phase.
// If feedback is non-empty the plan must address that feedback.
func buildPlanPrompt(ticketID, description, feedback string) string {
	feedbackSection := ""
	if feedback != "" {
		feedbackSection = fmt.Sprintf(`
HUMAN FEEDBACK ON PREVIOUS PLAN (you MUST address all points):
%s

`, feedback)
	}
	return fmt.Sprintf(`You are a Golem developer running autonomously.
The brainstorm spec has been approved. Complete the PLAN phase only.

Ticket ID: %s
Description: %s
Spec: .golem/tickets/%s/spec.md
%s
Instructions:
1. Read .golem/roles/developer.md for planning guidelines.
2. Read the spec: cat .golem/tickets/%s/spec.md
3. Write an ordered implementation plan to .golem/tickets/%s/plan.md.
   The plan must be detailed enough for autonomous implementation.
   Each step must include:
   - What to do (clear description)
   - Exact commands or code snippets to run/write (shell blocks, language-appropriate code snippets)
   - Expected diff line count
   Format: use markdown with ## headings per step and fenced code blocks.
4. Log the completion:
   golem log emit --ticket %s --role developer --type STATUS "Plan complete: <one-line summary>"

STOP after step 4. Do NOT run golem ticket advance or begin any implementation.
A human will review your plan in the orchestrator UI and approve before implementation begins.`,
		ticketID, description, ticketID, feedbackSection, ticketID, ticketID, ticketID)
}

// buildImplementPrompt returns the prompt for the implement Claude session.
// Claude implements the plan and runs review — it does NOT close the ticket.
func buildImplementPrompt(ticketID, description string) string {
	return fmt.Sprintf(`You are a Golem developer running autonomously.
The plan has been approved. IMPLEMENT this ticket fully.

Ticket ID: %s
Description: %s
Worktree: .golem/tickets/%s/worktree/  (checked out on branch ticket/%s)
Plan: .golem/tickets/%s/plan.md
Spec: .golem/tickets/%s/spec.md

Instructions:
1. Read .golem/roles/developer.md and follow the developer identity precisely.
2. Read the plan: cat .golem/tickets/%s/plan.md
3. For each plan step:
   a. Set the step: golem ticket set-step --ticket %s --expected-lines <n>
   b. Implement changes in the worktree directory.
   c. Commit at each logical unit (use git -C .golem/tickets/%s/worktree or cd into it).
   d. After each commit: golem ticket check-bloat
   e. Run golem observer dispatch calls per .golem/roles/developer.md
   f. Check .golem/tickets/%s/log.jsonl for unresolved BLOCKERs before continuing.
4. When all steps are complete: golem ticket review --ticket %s

STOP after the review. Do NOT run golem ticket close.
A human will review the work in the orchestrator UI and close the ticket.`,
		ticketID, description,
		ticketID, ticketID, ticketID, ticketID,
		ticketID, ticketID, ticketID, ticketID, ticketID)
}

// buildRevisePrompt returns the prompt for a revise session: the ticket
// was already implemented and reviewed once; this addresses human
// feedback given on that review in the same worktree/branch, then
// re-runs the review gate. Unlike buildImplementPrompt it forbids
// golem ticket close (the ticket returns to ready-for-review, not closed)
// and does not restate the plan — feedback is scoped to fixes, not a
// re-implementation.
func buildRevisePrompt(ticketID, description, feedback string) string {
	return fmt.Sprintf(`You are a Golem developer addressing review feedback.
This ticket was already implemented and reviewed once. A human reviewed
the work and requested changes. Address ALL of the feedback below in the
existing worktree, on the existing branch — do NOT start over.

Ticket ID: %s
Description: %s
Worktree: .golem/tickets/%s/worktree/  (checked out on branch ticket/%s)

Human feedback on the review:
%s

Instructions:
1. Read .golem/roles/developer.md and follow the developer identity precisely.
2. Address every point in the feedback above. Commit at each logical unit
   (use git -C .golem/tickets/%s/worktree or cd into it).
3. After each commit: golem ticket check-bloat
4. Run golem observer dispatch calls per .golem/roles/developer.md
5. Check .golem/tickets/%s/log.jsonl for unresolved BLOCKERs before continuing.
6. When the feedback is fully addressed: golem ticket review --ticket %s

STOP after the review. Do NOT run golem ticket close.
A human will review the new changes in the orchestrator UI.`,
		ticketID, description, ticketID, ticketID,
		feedback,
		ticketID, ticketID, ticketID)
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
	defer f.Close()

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
