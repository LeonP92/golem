package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/ticket"
)

func gitCommit(t *testing.T, dir, file, content, message string) string {
	t.Helper()
	os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("add", file)
	run("commit", "-q", "-m", message)
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = dir
	out, err := shaCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func setUpTicketForStepTest(t *testing.T) (repo, worktree, ticketID string) {
	t.Helper()
	repo = initRepoForCLI(t)
	ticketID = "t1"
	s := ticket.New(ticketID, "", false)
	worktree = filepath.Join(repo, ".golem", "tickets", ticketID, "worktree")
	os.MkdirAll(worktree, 0o755)
	cmd := exec.Command("git", "clone", "-q", repo, worktree)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	// Set git config in the cloned worktree
	for _, pair := range [][]string{{"user.email", "test@example.com"}, {"user.name", "test"}} {
		c := exec.Command("git", "config", pair[0], pair[1])
		c.Dir = worktree
		c.Run()
	}
	s.WorktreePath = worktree
	if err := s.Save(filepath.Join(repo, ".golem", "tickets", ticketID)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return repo, worktree, ticketID
}

func TestSetStepUpdatesExpectedLines(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	var stdout, stderr bytes.Buffer

	code := SetStep([]string{"--repo", repo, "--ticket", ticketID, "--expected-lines", "10"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("SetStep failed: exit %d, stderr=%s", code, stderr.String())
	}
	s, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", ticketID))
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	if s.CurrentStepExpectedLines != 10 {
		t.Errorf("CurrentStepExpectedLines = %d, want 10", s.CurrentStepExpectedLines)
	}
}

func TestCheckBloatFlagsCommitFarOverExpectation(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	SetStep([]string{"--repo", repo, "--ticket", ticketID, "--expected-lines", "2"}, &bytes.Buffer{}, &bytes.Buffer{})

	bigContent := strings.Repeat("line of code\n", 50)
	sha := gitCommit(t, worktree, "feature.go", bigContent, "add feature")

	var stdout, stderr bytes.Buffer
	code := CheckBloat([]string{"--repo", repo, "--ticket", ticketID, "--commit", sha}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("CheckBloat failed: exit %d, stderr=%s", code, stderr.String())
	}

	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.Contains(string(e.Type), "FINDING") && strings.Contains(e.Message, "SCOPE_BLOAT") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a SCOPE_BLOAT finding for a 50-line commit against a 2-line expectation, got entries: %+v", entries)
	}
}

// gitCommitFiles commits several files (creating parent dirs) in one commit.
func gitCommitFiles(t *testing.T, dir string, files map[string]string, message string) string {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	add := exec.Command("git", "add", "-A")
	add.Dir = dir
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	commit := exec.Command("git", "commit", "-q", "-m", message)
	commit.Dir = dir
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = dir
	out, err := shaCmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func assertNoFindings(t *testing.T, repo, ticketID string) {
	t.Helper()
	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no findings, got %+v", entries)
	}
}

// Regression: a Pact consumer test whose plan said "~150 lines + 1 generated
// JSON fixture" was flagged because the fixture's lines were counted too.
func TestCheckBloatIgnoresGeneratedFiles(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	SetStep([]string{"--repo", repo, "--ticket", ticketID, "--expected-lines", "10"}, &bytes.Buffer{}, &bytes.Buffer{})

	sha := gitCommitFiles(t, worktree, map[string]string{
		"consumer_test.go":           strings.Repeat("line of code\n", 10),
		"pacts/bff-svc.json":         strings.Repeat("{}\n", 200),
		"go.sum":                     strings.Repeat("mod h1:x\n", 200),
		"gen/account.pb.go":          strings.Repeat("// generated\n", 200),
		"__snapshots__/view.ts.snap": strings.Repeat("snap\n", 200),
	}, "add pact test + fixture")

	var stdout, stderr bytes.Buffer
	if code := CheckBloat([]string{"--repo", repo, "--ticket", ticketID, "--commit", sha}, &stdout, &stderr); code != 0 {
		t.Fatalf("CheckBloat failed: exit %d, stderr=%s", code, stderr.String())
	}
	assertNoFindings(t, repo, ticketID)
}

func TestCheckBloatHonoursLinguistGeneratedAttribute(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	gitCommitFiles(t, worktree, map[string]string{
		".gitattributes": "fixtures/** linguist-generated\n",
	}, "mark fixtures generated")
	SetStep([]string{"--repo", repo, "--ticket", ticketID, "--expected-lines", "10"}, &bytes.Buffer{}, &bytes.Buffer{})

	sha := gitCommitFiles(t, worktree, map[string]string{
		"feature.go":         strings.Repeat("line of code\n", 10),
		"fixtures/dump.yaml": strings.Repeat("k: v\n", 200),
	}, "add feature + fixture")

	var stdout, stderr bytes.Buffer
	if code := CheckBloat([]string{"--repo", repo, "--ticket", ticketID, "--commit", sha}, &stdout, &stderr); code != 0 {
		t.Fatalf("CheckBloat failed: exit %d, stderr=%s", code, stderr.String())
	}
	assertNoFindings(t, repo, ticketID)
}

func TestCountChangedLinesSkipsBinaryAndCountsRenamesByContent(t *testing.T) {
	_, worktree, _ := setUpTicketForStepTest(t)
	gitCommitFiles(t, worktree, map[string]string{
		"old.go": strings.Repeat("line of code\n", 50),
	}, "seed")

	mv := exec.Command("git", "mv", "old.go", "new.go")
	mv.Dir = worktree
	if out, err := mv.CombinedOutput(); err != nil {
		t.Fatalf("git mv: %v\n%s", err, out)
	}
	sha := gitCommitFiles(t, worktree, map[string]string{
		"new.go":    strings.Repeat("line of code\n", 50) + "one more\n",
		"image.bin": "\x00\x01\x02binary",
	}, "rename + binary")

	got, err := countChangedLines(worktree, sha)
	if err != nil {
		t.Fatalf("countChangedLines: %v", err)
	}
	if got != 1 {
		t.Errorf("countChangedLines = %d, want 1 (rename is +1 line, binary is uncounted)", got)
	}
}

func TestCheckBloatStaysSilentWithinExpectation(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	SetStep([]string{"--repo", repo, "--ticket", ticketID, "--expected-lines", "10"}, &bytes.Buffer{}, &bytes.Buffer{})

	sha := gitCommit(t, worktree, "feature.go", "package main\n", "add feature")

	var stdout, stderr bytes.Buffer
	code := CheckBloat([]string{"--repo", repo, "--ticket", ticketID, "--commit", sha}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("CheckBloat failed: exit %d, stderr=%s", code, stderr.String())
	}
	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no findings for a small commit within expectation, got %+v", entries)
	}
}
