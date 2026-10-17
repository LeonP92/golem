package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/ticket"
)

func initRepoForCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644)
	golemDir := filepath.Join(dir, ".golem")
	os.MkdirAll(filepath.Join(golemDir, "tickets"), 0o755)
	os.WriteFile(filepath.Join(golemDir, "config.yaml"), []byte("backend: claude-code\ngate:\n  commands: []\n"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "initial")
	return dir
}

func TestTicketNewCreatesStateWorktreeAndLog(t *testing.T) {
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer

	code := TicketNew([]string{"--repo", repo, "--id", "t1", "add-widget"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketNew failed: exit %d, stderr=%s", code, stderr.String())
	}

	ticketDir := filepath.Join(repo, ".golem", "tickets", "t1")
	s, err := ticket.Load(ticketDir)
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	if s.Branch != "ticket/t1" {
		t.Errorf("Branch = %q, want ticket/t1", s.Branch)
	}
	if _, err := os.Stat(s.WorktreePath); err != nil {
		t.Fatalf("worktree missing: %v", err)
	}

	entries, err := blog.ReadAll(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("blog.ReadAll: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != blog.TypeStatus {
		t.Fatalf("expected one STATUS entry, got %+v", entries)
	}
}

func TestTicketNewTrivialFlagSkipsBrainstorm(t *testing.T) {
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer

	code := TicketNew([]string{"--repo", repo, "--id", "t2", "--trivial", "fix-typo"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketNew failed: exit %d, stderr=%s", code, stderr.String())
	}
	s, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", "t2"))
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	if s.Phase != ticket.PhasePlan {
		t.Errorf("Phase = %q, want plan for a trivial ticket", s.Phase)
	}
}

func TestTicketNew_WithTicketID(t *testing.T) {
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer

	code := TicketNew([]string{"--repo", repo, "--ticket-id", "42", "fix the bug"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".golem", "tickets", "42")); os.IsNotExist(err) {
		t.Error("expected ticket dir .golem/tickets/42 to exist")
	}
}

// TestTicketNew_DescriptionStartingWithDash proves the real flag.FlagSet
// honours the "--" separator the shem worker now passes (finding S3), and
// that the description survives it byte-for-byte.
//
// Verified against HEAD 51d9526 without the separator: "- [ ] add dark mode"
// gave "flag provided but not defined" and exit 1; "---\ntitle: bug\n---"
// gave "bad flag syntax" and exit 1; and a body of exactly "--from-issue=7"
// parsed as that flag and reached the GitHub-fetch path, stopped only by
// GOLEM_GITHUB_TOKEN being unset. Any of those is an issue that can never be
// worked: the worker posts needs-attention, a human requeues, and it fails
// identically.
func TestTicketNew_DescriptionStartingWithDash(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{name: "markdown bullet", description: "- add dark mode"},
		{name: "markdown checklist", description: "- [ ] add dark mode\n- [ ] add tests"},
		{name: "horizontal rule and front matter", description: "---\ntitle: bug\n---\nIt crashes."},
		{name: "single-dash flag lookalike", description: "-trivial"},
		{name: "flag injection attempt", description: "--from-issue=7"},
		{name: "repo flag injection attempt", description: "--repo=/tmp"},
		{name: "ordinary prose is unaffected", description: "Add a dark mode toggle"},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoForCLI(t)
			id := fmt.Sprintf("dash-%d", i)
			var stdout, stderr bytes.Buffer

			args := []string{"--repo", repo, "--ticket-id", id, "--branch", "b" + id, "--", tc.description}
			if code := TicketNew(args, &stdout, &stderr); code != 0 {
				t.Fatalf("TicketNew exit %d, want 0: stderr=%s", code, stderr.String())
			}

			s, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", id))
			if err != nil {
				t.Fatalf("ticket.Load: %v", err)
			}
			if s.Description != tc.description {
				t.Errorf("Description = %q, want %q", s.Description, tc.description)
			}
			// The GitHub-fetch path must not have been taken: it would have
			// replaced the description and stamped an issue number.
			if s.IssueNumber != 0 {
				t.Errorf("IssueNumber = %d, want 0 — the description was parsed as --from-issue",
					s.IssueNumber)
			}
		})
	}
}

// A non-claude shem hands `ticket new` its own backend document, because
// WorktreeSetup writes vendor-specific files.
func TestTicketNewUsesTheBackendDocumentsAdapter(t *testing.T) {
	repo := initRepoForCLI(t)
	doc := writeBackendDoc(t, "backend:\n  adapter: claude-code\n")
	var stdout, stderr bytes.Buffer

	code := TicketNew([]string{"--repo", repo, "--id", "t1", "--backend-config", doc, "--", "add-widget"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketNew failed: exit %d, stderr=%s", code, stderr.String())
	}
	s, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", "t1"))
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	// claude-code's WorktreeSetup writes .claude/settings.json.
	if _, err := os.Stat(filepath.Join(s.WorktreePath, ".claude", "settings.json")); err != nil {
		t.Errorf("the document's adapter did not run WorktreeSetup: %v", err)
	}
}

func TestTicketNewBasePinsWorktreeToCommit(t *testing.T) {
	repo := initRepoForCLI(t)
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	baseSHA := git("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(repo, "SECOND.md"), []byte("later"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "second")

	var stdout, stderr bytes.Buffer
	code := TicketNew([]string{"--repo", repo, "--id", "tb", "--base", baseSHA, "add-widget"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketNew failed: exit %d, stderr=%s", code, stderr.String())
	}
	s, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", "tb"))
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	if s.BranchBaseSHA != baseSHA {
		t.Errorf("BranchBaseSHA = %q, want %q", s.BranchBaseSHA, baseSHA)
	}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = s.WorktreePath
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse in worktree: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != baseSHA {
		t.Errorf("worktree HEAD = %q, want pinned base %q", got, baseSHA)
	}
}

func TestTicketNewRejectsUnresolvableBase(t *testing.T) {
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer
	for _, flag := range []string{"--base"} {
		code := TicketNew([]string{"--repo", repo, "--id", "tb", flag, "does-not-exist", "add-widget"}, &stdout, &stderr)
		if code == 0 {
			t.Errorf("TicketNew with %s does-not-exist: expected failure, got exit 0", flag)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, ".golem", "tickets", "tb", "state.json")); !os.IsNotExist(err) {
		t.Error("failed base resolution must not leave a ticket behind")
	}
}

func TestTicketNewRecordsAcceptanceCommands(t *testing.T) {
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer
	if code := TicketNew([]string{"--repo", repo, "--id", "ta", "--accept", "sh a.sh", "--accept", "sh b.sh", "x"}, &stdout, &stderr); code != 0 {
		t.Fatalf("TicketNew: exit %d, stderr=%s", code, stderr.String())
	}
	s, _ := ticket.Load(filepath.Join(repo, ".golem", "tickets", "ta"))
	if len(s.AcceptanceCommands) != 2 || s.AcceptanceCommands[1] != "sh b.sh" {
		t.Fatalf("AcceptanceCommands = %q", s.AcceptanceCommands)
	}
}
