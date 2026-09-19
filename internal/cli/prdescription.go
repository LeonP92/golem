package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/roles"
	"github.com/leonp92/golem/internal/ticket"
)

// maxPRBody caps the generated body. GitHub rejects a pull request body over
// 65536 characters, and a description that long is a failure of the role
// prompt rather than something to pass along.
const maxPRBody = 60000

// TicketPRDescription writes a pull request body for a ticket to stdout.
//
// A separate command rather than something the orchestrator does, because the
// orchestrator has no checkout: the description is written from the branch's
// own diff and the ticket's log, both of which exist only on the shem. The
// shem runs this after a successful push and hands the result back with the
// branch-pushed report.
//
// Failure is not fatal to the caller by design — the shem falls back to the
// minimal body so the pull request still opens. A missing description is an
// inconvenience; a missing pull request is lost work.
func TicketPRDescription(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket pr-description", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	issue := fs.Int("issue", 0, "GitHub issue number this closes (0 for none)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket pr-description --ticket <id> [--issue <n>]")
		return 1
	}

	golemDir := filepath.Join(*repo, ".golem")
	cfg, err := config.Load(filepath.Join(golemDir, "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	ticketDir := filepath.Join(golemDir, "tickets", *id)
	s, err := ticket.Load(ticketDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading ticket %s: %v\n", *id, err)
		return 1
	}
	// The repository's own role file wins, but falls back to the embedded
	// default rather than failing. golem init only unpacks roles when a repo
	// has no .golem at all, so an already-initialised repository would never
	// receive a newly shipped role — and this feature would silently do
	// nothing on precisely the repositories already in use.
	rolePrompt, err := os.ReadFile(filepath.Join(golemDir, "roles", "pr-description.md"))
	if os.IsNotExist(err) {
		rolePrompt, err = roles.Defaults.ReadFile("defaults/pr-description.md")
	}
	if err != nil {
		fmt.Fprintf(stderr, "reading pr-description role: %v\n", err)
		return 1
	}
	entries, err := blog.ReadAll(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "reading log: %v\n", err)
		return 1
	}

	// The log is the evidence for the Automated tests section: it is where
	// the commands the agent actually ran were recorded. Handing it over is
	// what lets the role report real results instead of guessing at them.
	prompt := string(rolePrompt)
	if *issue > 0 {
		prompt += fmt.Sprintf("\n\n## This pull request\n\nIt closes issue #%d. "+
			"Put `Closes #%d` on its own line at the end of the Outcome section, "+
			"and use no other issue number anywhere.\n", *issue, *issue)
	} else {
		// No number given: the caller (the orchestrator) appends the closing
		// reference from the ticket row, which is the only place that knows
		// it. The role must not guess at one.
		prompt += "\n\n## This pull request\n\nYou have not been given an issue number. " +
			"Do not write a `Closes` line and do not invent an issue number; one is " +
			"added by the caller when there is an issue to close.\n"
	}

	runner, err := NewRunner(cfg, s.WorktreePath)
	if err != nil {
		fmt.Fprintf(stderr, "selecting backend: %v\n", err)
		return 1
	}
	result, err := runner.RunAgent("pr-description", agentrunner.Context{
		LogSlice:   entries,
		RolePrompt: prompt,
	})
	if err != nil {
		fmt.Fprintf(stderr, "running pr-description: %v\n", err)
		return 1
	}

	body := strings.TrimSpace(result.Output)
	if body == "" {
		fmt.Fprintln(stderr, "pr-description produced an empty body")
		return 1
	}
	if len(body) > maxPRBody {
		body = body[:maxPRBody] + "\n\n_(truncated by golem: the generated body exceeded GitHub's limit)_"
	}
	fmt.Fprintln(stdout, body)
	return 0
}
