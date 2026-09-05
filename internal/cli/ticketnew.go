package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/ticket"
	"github.com/leonp92/golem/internal/workspace"
)

func TicketNew(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("id", "", "ticket id (required)")
	ticketID := fs.String("ticket-id", "", "ticket id assigned by orchestrator (alternative to --id)")
	trivial := fs.Bool("trivial", false, "skip brainstorm, go straight to plan with developer+reviewer only")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *ticketID != "" {
		*id = *ticketID
	}
	description := strings.Join(fs.Args(), " ")
	if *id == "" || description == "" {
		fmt.Fprintln(stderr, "usage: golem ticket new (--id | --ticket-id) <id> <description>")
		fmt.Fprintln(stderr, "  --id: ticket id (can be omitted for human users, required for orchestrator)")
		fmt.Fprintln(stderr, "  --ticket-id: alias for --id used by Shem workers; if both provided, --ticket-id wins")
		return 1
	}

	cfg, err := config.Load(filepath.Join(*repo, ".golem", "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	runner, err := NewRunner(cfg, *repo)
	if err != nil {
		fmt.Fprintf(stderr, "initialising runner: %v\n", err)
		return 1
	}

	s := ticket.New(*id, description, *trivial)
	worktreePath, branch, err := workspace.Create(*repo, *id, "HEAD")
	if err != nil {
		fmt.Fprintf(stderr, "creating worktree: %v\n", err)
		return 1
	}
	if err := runner.WorktreeSetup(worktreePath); err != nil {
		fmt.Fprintf(stderr, "worktree setup: %v\n", err)
		return 1
	}
	s.Branch = branch
	s.WorktreePath = worktreePath

	ticketDir := filepath.Join(*repo, ".golem", "tickets", *id)
	if err := s.Save(ticketDir); err != nil {
		fmt.Fprintf(stderr, "saving ticket state: %v\n", err)
		return 1
	}

	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "opening ticket log: %v\n", err)
		return 1
	}
	defer w.Close()
	if err := w.Append(blog.NewEntry("system", blog.TypeStatus, "ticket created: "+description)); err != nil {
		fmt.Fprintf(stderr, "writing initial log entry: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "created ticket %s on branch %s\n", *id, branch)
	return 0
}
