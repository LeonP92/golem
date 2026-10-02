package cli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/models"
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
	base := fs.String("base", "", "review everything from the merge-base with this ref up to --commit, not --commit alone")
	mf := addModelFlags(fs)
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
	if err := mf.validate(cfg); err != nil {
		fmt.Fprintln(stderr, err)
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

	diff, err := reviewDiff(s.WorktreePath, *base, *commit, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "getting commit diff: %v\n", err)
		return 1
	}
	if strings.TrimSpace(diff) == "" {
		fmt.Fprintf(stdout, "nothing to review for %s at %s\n", *role, *commit)
		return 0
	}

	runner, err := mf.runner(cfg, s.WorktreePath)
	if err != nil {
		fmt.Fprintf(stderr, "selecting backend: %v\n", err)
		return 1
	}
	model, err := mf.resolve(cfg, *role, models.StageObserve)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	obs := observer.New(filepath.Join(ticketDir, "log.jsonl"), runner, model)
	if err := obs.DispatchForCommit(*role, *commit, diff, string(rolePrompt)); err != nil {
		fmt.Fprintf(stderr, "dispatch: %v\n", err)
		return 1
	}
	return 0
}

// reviewDiff is the one commit when base is empty, and otherwise the whole
// branch: three dots diffs from the merge-base, so a base that has moved on
// since the branch was cut contributes nothing of its own.
//
// A base this checkout does not have falls back to the one commit, said out
// loud: a narrower review beats none, and failing here would silently skip
// every observer on the ticket.
func reviewDiff(worktreePath, base, commit string, stderr io.Writer) (string, error) {
	if base == "" {
		return commitDiff(worktreePath, commit)
	}
	verify := exec.Command("git", "rev-parse", "--verify", "--quiet", base+"^{commit}")
	verify.Dir = worktreePath
	if verify.Run() != nil {
		fmt.Fprintf(stderr, "base %s is not in this checkout; reviewing %s alone\n", base, commit)
		return commitDiff(worktreePath, commit)
	}
	cmd := exec.Command("git", "diff", base+"..."+commit)
	cmd.Dir = worktreePath
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff %s...%s: %w: %s", base, commit, err, strings.TrimSpace(errOut.String()))
	}
	return string(out), nil
}
