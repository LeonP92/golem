package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/observer"
	"github.com/leonp92/golem/internal/ticket"
)

func ObserverDispatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("observer dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	role := fs.String("role", "", "watcher role to dispatch (required)")
	commit := fs.String("commit", "", "commit SHA to review (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" || *role == "" || *commit == "" {
		fmt.Fprintln(stderr, "usage: golem observer dispatch --ticket <id> --role <role> --commit <sha>")
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

	rolePrompt, err := os.ReadFile(filepath.Join(golemDir, "roles", *role+".md"))
	if err != nil {
		fmt.Fprintf(stderr, "reading role prompt: %v\n", err)
		return 1
	}

	diff, err := commitDiff(s.WorktreePath, *commit)
	if err != nil {
		fmt.Fprintf(stderr, "getting commit diff: %v\n", err)
		return 1
	}

	runner, err := NewRunner(cfg, s.WorktreePath)
	if err != nil {
		fmt.Fprintf(stderr, "selecting backend: %v\n", err)
		return 1
	}

	obs := observer.New(filepath.Join(ticketDir, "log.jsonl"), runner)
	if err := obs.DispatchForCommit(*role, *commit, diff, string(rolePrompt)); err != nil {
		fmt.Fprintf(stderr, "dispatch: %v\n", err)
		return 1
	}
	return 0
}
