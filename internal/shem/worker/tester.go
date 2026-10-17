package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/roles"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/tester"
	"github.com/leonp92/golem/internal/ticket"
)

// runTesterStage runs the tester role on the head just pushed and posts
// its verdict. Failures are reported on the ticket and swallowed: the
// lane simply waits for a verdict.
func (e *GolemExecutor) runTesterStage(ctx context.Context, c *client.Client,
	claim *client.ClaimResponse, repoPath, worktree, ticketDir string) {

	head, herr := worktreeHead(ctx, worktree)
	if herr != nil || head == "" {
		postStatus(c, claim.TicketID, "Tester stage skipped: no readable head in the worktree")
		return
	}

	role, err := os.ReadFile(filepath.Join(repoPath, ".golem", "roles", "tester.md"))
	if err != nil {
		// A repo initialized before the tester existed has no role file;
		// the embedded default is the same text the unpack would write.
		if def, derr := roles.Defaults.ReadFile("defaults/tester.md"); derr == nil {
			role = def
		} else {
			postStatus(c, claim.TicketID, "Tester stage skipped: no tester role available")
			return
		}
	}

	prompt, err := buildTesterPrompt(ctx, string(role), claim, worktree, ticketDir, head)
	if err != nil {
		postStatus(c, claim.TicketID, "Tester stage could not read candidate evidence: "+firstLineOf(err.Error()))
		return
	}

	logPath := filepath.Join(ticketDir, "agent-tester.log")
	f, err := os.Create(logPath) //nolint:gosec
	if err != nil {
		postStatus(c, claim.TicketID, "Tester stage could not open its log: "+err.Error())
		return
	}
	defer func() { _ = f.Close() }()
	var buf bytes.Buffer
	model := e.claimModel(c, claim, models.StageTest)
	postStatus(c, claim.TicketID, "Tester stage running against "+shortSHA(head))
	if err := e.Agent.Adapter.RunPhase(ctx, worktree, prompt, io.MultiWriter(f, &buf), model); err != nil {
		postStatus(c, claim.TicketID, "Tester stage failed to run: "+firstLineOf(err.Error()))
		log.Printf("executor: tester stage for %s: %v", claim.TicketID, err)
		return
	}

	v, found := tester.ParseVerdict(buf.String())
	if !found {
		// No readable verdict is not a pass: record nothing, and the lane
		// stays blocked. The ticket log says why.
		postStatus(c, claim.TicketID, "Tester stage produced no readable verdict; auto-merge stays blocked")
		return
	}
	// The posted head is the one measured, never the agent's claim; a
	// mismatch or a head that moved during the run is not a pass.
	verdict := strings.ToLower(v.Verdict)
	if after, _ := worktreeHead(ctx, worktree); v.HeadSHA != head || after != head {
		postStatus(c, claim.TicketID, fmt.Sprintf("Tester reported head %s, candidate is %s (now %s); recording as inconclusive",
			shortSHA(v.HeadSHA), shortSHA(head), shortSHA(after)))
		verdict = "inconclusive"
	}
	if err := c.PostEpicVerdict(claim.Epic.EpicID, client.EpicVerdictPayload{
		EpicNodeKey: claim.Epic.NodeKey,
		HeadSHA:     head,
		BaseSHA:     v.BaseSHA,
		Verdict:     verdict,
		Scenarios:   v.Scenarios,
		Evidence:    v.Evidence,
		Backend:     model,
	}); err != nil {
		postStatus(c, claim.TicketID, "Tester verdict could not be recorded: "+firstLineOf(err.Error()))
		log.Printf("executor: post tester verdict for %s: %v", claim.TicketID, err)
		return
	}
	postStatus(c, claim.TicketID, "Tester verdict recorded: "+verdict)
}

// Supply the source as data: a tester that cannot run tools still needs the
// exact comparison base and the complete candidate diff to judge correctness.
func buildTesterPrompt(ctx context.Context, role string, claim *client.ClaimResponse, worktree, ticketDir, head string) (string, error) {
	state, err := ticket.Load(ticketDir)
	if err != nil {
		return "", err
	}
	if state.BranchBaseSHA == "" {
		return "", fmt.Errorf("candidate has no pinned base SHA")
	}
	cmd := asAgent(exec.CommandContext(ctx, "git", "diff", state.BranchBaseSHA+"..."+head))
	cmd.Dir = worktree
	diff, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("reading candidate diff: %w", err)
	}
	evidence := fmt.Sprintf("Repository: %s\nHead SHA under test: %s\nBase SHA: %s\n\nAcceptance criteria:\n%s\n", claim.RepoRemote, head, state.BranchBaseSHA, claim.Description)
	evidence += acceptanceEvidence(ctx, worktree, claim.Epic.AcceptanceCommands)
	return agentrunner.BuildPrompt(agentrunner.Context{
		RolePrompt: role + tester.Contract + "\nYou cannot run commands in this session. Judge the candidate diff and acceptance results supplied as data. A non-zero acceptance exit is a FAIL.\n",
		Diff:       string(diff),
		LogSlice:   []blog.Entry{blog.NewEntry("worker", blog.TypeStatus, evidence)},
	}), nil
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// acceptanceEvidence runs the node's acceptance commands in the tester
// sandbox (no secrets; no network where the host can isolate it) and
// renders the results for the tester, which cannot execute anything itself.
func acceptanceEvidence(ctx context.Context, worktree string, cmds []string) string {
	if len(cmds) == 0 {
		return "\n## Acceptance results\n\n(no acceptance commands)\n"
	}
	var b strings.Builder
	b.WriteString("\n## Acceptance results (run by the worker in the tester sandbox)\n\n")
	for _, c := range cmds {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		cmd, isolated := tester.Command(cctx, worktree, "sh", "-c", c)
		out, err := cmd.CombinedOutput()
		cancel()
		exit := 0
		if err != nil {
			exit = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			}
		}
		if len(out) > 4000 {
			out = out[len(out)-4000:]
		}
		fmt.Fprintf(&b, "$ %s\nexit=%d network_isolated=%t\n%s\n\n", c, exit, isolated, out)
	}
	return b.String()
}
