package cli

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leonpham/golem/internal/blog"
	"github.com/leonpham/golem/internal/bloat"
	"github.com/leonpham/golem/internal/ticket"
)

func SetStep(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket set-step", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	expectedLines := fs.Int("expected-lines", 0, "expected diff line count for the step about to start")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket set-step --ticket <id> --expected-lines <n>")
		return 1
	}

	ticketDir := filepath.Join(*repo, ".golem", "tickets", *id)
	s, err := ticket.Load(ticketDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading ticket %s: %v\n", *id, err)
		return 1
	}
	s.CurrentStepExpectedLines = *expectedLines
	if err := s.Save(ticketDir); err != nil {
		fmt.Fprintf(stderr, "saving ticket: %v\n", err)
		return 1
	}
	return 0
}

func countChangedLines(worktreePath, sha string) (int, error) {
	cmd := exec.Command("git", "show", sha)
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	count := 0
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
			count++
		}
	}
	return count, scanner.Err()
}

func commitDiff(worktreePath, sha string) (string, error) {
	cmd := exec.Command("git", "show", sha)
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	return string(out), err
}

func CheckBloat(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket check-bloat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	commit := fs.String("commit", "", "commit SHA to check (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" || *commit == "" {
		fmt.Fprintln(stderr, "usage: golem ticket check-bloat --ticket <id> --commit <sha>")
		return 1
	}

	ticketDir := filepath.Join(*repo, ".golem", "tickets", *id)
	s, err := ticket.Load(ticketDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading ticket %s: %v\n", *id, err)
		return 1
	}

	actual, err := countChangedLines(s.WorktreePath, *commit)
	if err != nil {
		fmt.Fprintf(stderr, "counting changed lines: %v\n", err)
		return 1
	}

	exceeded, ratio := bloat.Check(actual, s.CurrentStepExpectedLines)
	if !exceeded {
		return 0
	}

	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "opening log: %v\n", err)
		return 1
	}
	defer w.Close()
	e := blog.NewEntry("system", blog.TypeFinding, fmt.Sprintf(
		"SCOPE_BLOAT: commit changed %d lines against a %d-line expectation (%.1fx)",
		actual, s.CurrentStepExpectedLines, ratio))
	e.CommitSHA = *commit
	if err := w.Append(e); err != nil {
		fmt.Fprintf(stderr, "writing finding: %v\n", err)
		return 1
	}
	return 0
}
