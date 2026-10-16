package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/gate"
	"github.com/leonp92/golem/internal/ticket"
)

// TicketVerify is the worker's self-verification: it runs the configured
// gate commands against the worktree BEFORE the review gate, and records
// the outcome as a self-verification entry bound to the exact head. The
// implementer catching its own breakage here costs one local run; the
// same failure found by the reviewer or the tester costs a full loop.
//
// Self-verification never substitutes for the independent gates: the
// review gate runs the same commands again at review time, and the tester
// verdict stays the only auto-merge authorization. This command only
// records evidence and answers "did the worker's own checks pass".
//
// Exit codes: 0 = checks passed, 1 = could not run, 2 = a check failed.
func TicketVerify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket verify --ticket <id>")
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

	res, err := gate.Run(s.WorktreePath, ticketGate(cfg, s))
	if err != nil {
		fmt.Fprintf(stderr, "running verification: %v\n", err)
		return 1
	}

	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "opening log: %v\n", err)
		return 1
	}
	// The entry names the head so a later reader can tell verification of
	// this code from verification of an ancestor — the same staleness rule
	// the review gate's evidence follows.
	entry := blog.NewEntry("worker", blog.TypeStatus, fmt.Sprintf(
		"self-verification: %t (%d command(s)) at %s\n%s",
		res.Passed, res.CommandsRun, res.HeadSHA, res.Output))
	if err := w.Append(entry); err != nil {
		closeLogWriter(w, stderr)
		fmt.Fprintf(stderr, "recording verification: %v\n", err)
		return 1
	}
	closeLogWriter(w, stderr)

	fmt.Fprint(stdout, res.Output)
	if !res.Passed {
		return 2
	}
	return 0
}

// ticketGate is the repo's gate plus the ticket's own acceptance commands.
func ticketGate(cfg *config.Config, s *ticket.State) config.GateConfig {
	g := cfg.Gate
	g.Commands = append(append([]string{}, cfg.Gate.Commands...), s.AcceptanceCommands...)
	return g
}
