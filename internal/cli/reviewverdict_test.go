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

func TestParseReviewerVerdict(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		wantPass  bool
		wantFound bool
	}{
		{"pass", "all good\nVERDICT: PASS", true, true},
		{"fail", "found a blocker\nVERDICT: FAIL", false, true},
		{"missing", "attested: convention=pass", false, false},
		{"empty", "", false, false},
		{"last verdict wins", "VERDICT: PASS\nmore checking\nVERDICT: FAIL", false, true},
		{"paraphrase does not count", "verdict: pass\nVERDICT:PASS", false, false},
		{"extra words do not count", "VERDICT: PASS with reservations", false, false},
		{"markdown emphasis", "**VERDICT: PASS**", true, true},
		{"bold word", "VERDICT: **FAIL**", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pass, found := parseReviewerVerdict(tc.output)
			if pass != tc.wantPass || found != tc.wantFound {
				t.Errorf("parseReviewerVerdict(%q) = (%t, %t), want (%t, %t)",
					tc.output, pass, found, tc.wantPass, tc.wantFound)
			}
		})
	}
}

// A reviewer that rejects must stop the ticket even when every gate
// command passes.
func TestTicketReviewRejectedReviewerCannotPass(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	fakeClaudeOnPath(t, "attested: correctness=fail — the migration drops a column\nVERDICT: FAIL")

	var stdout, stderr bytes.Buffer
	code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketReview failed: exit %d, stderr=%s", code, stderr.String())
	}
	loaded, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", ticketID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Phase != ticket.PhaseNeedsAttention {
		t.Errorf("Phase = %q, want needs-attention when the reviewer rejects", loaded.Phase)
	}
}

// No verdict line: an ordinary ticket keeps the gate-only rule, but a
// ticket with acceptance commands (an epic child) cannot advance.
func TestTicketReviewMissingVerdict(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	ticketDir := filepath.Join(repo, ".golem", "tickets", ticketID)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	fakeClaudeOnPath(t, "looks fine to me")

	review := func() ticket.Phase {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 0 {
			t.Fatalf("TicketReview failed: exit %d, stderr=%s", code, stderr.String())
		}
		loaded, err := ticket.Load(ticketDir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return loaded.Phase
	}
	if got := review(); got != ticket.PhaseReadyForReview {
		t.Errorf("ordinary ticket: Phase = %q, want ready-for-review (gate-only, as before)", got)
	}
	s, _ := ticket.Load(ticketDir)
	s.AcceptanceCommands = []string{"true"}
	s.Save(ticketDir)
	if got := review(); got != ticket.PhaseNeedsAttention {
		t.Errorf("epic child: Phase = %q, want needs-attention when the verdict is missing", got)
	}
}

// The gate evidence is recorded against the exact head it ran on, so a
// later merge decision can tell a current pass from a stale one.
func TestTicketReviewRecordsGateEvidenceBoundToHead(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	fakeClaudeOnPath(t, "VERDICT: PASS")

	var stdout, stderr bytes.Buffer
	if code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 0 {
		t.Fatalf("TicketReview failed: exit %d, stderr=%s", code, stderr.String())
	}
	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var evidence *blog.Entry
	for i := range entries {
		if entries[i].Role == "gate" {
			evidence = &entries[i]
		}
	}
	if evidence == nil {
		t.Fatal("no gate evidence entry recorded")
	}
	head, err := gitHead(worktree)
	if err != nil {
		t.Fatalf("gitHead: %v", err)
	}
	if !bytes.Contains([]byte(evidence.Message), []byte("head="+head)) {
		t.Errorf("gate evidence not bound to the worktree head %s: %s", head, evidence.Message)
	}
	if !bytes.Contains([]byte(evidence.Message), []byte("commands=")) {
		t.Errorf("gate evidence does not record the executed command count: %s", evidence.Message)
	}
}

// Backwards compatibility: an install whose unpacked reviewer.md predates
// the verdict section (role files are never overwritten once unpacked)
// must still produce a parseable verdict, because the contract travels
// with the invocation.
func TestTicketReviewAppendsVerdictContractToOldRoleFiles(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	// A pre-contract role file: no VERDICT instructions anywhere in it.
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"),
		[]byte("# Reviewer Role\n\nYou run the final holistic pass.\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	promptPath := fakeClaudeRecordingPrompt(t, "attested: convention=pass\nVERDICT: PASS")

	var stdout, stderr bytes.Buffer
	if code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 0 {
		t.Fatalf("TicketReview failed: exit %d, stderr=%s", code, stderr.String())
	}
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("reading recorded prompt: %v", err)
	}
	if !bytes.Contains(prompt, []byte("VERDICT: PASS")) || !bytes.Contains(prompt, []byte("Output contract")) {
		t.Error("reviewer prompt does not carry the verdict contract; old role files would break the gate")
	}
	loaded, err := ticket.Load(filepath.Join(repo, ".golem", "tickets", ticketID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Phase != ticket.PhaseReadyForReview {
		t.Errorf("Phase = %q, want ready-for-review with the contract appended", loaded.Phase)
	}
}

// The reviewer's verdict gates the ticket, so it must see the branch's
// changes since the pinned base, not just the log.
func TestTicketReviewGivesReviewerTheBranchDiff(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	git := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = worktree
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := git("rev-parse", "HEAD")
	for i, body := range []string{"FIRST-CHANGE-MARKER", "SECOND-CHANGE-MARKER"} {
		os.WriteFile(filepath.Join(worktree, fmt.Sprintf("f%d.txt", i)), []byte(body+"\n"), 0o644)
		git("add", "-A")
		git("commit", "-qm", "change")
	}
	ticketDir := filepath.Join(repo, ".golem", "tickets", ticketID)
	s, _ := ticket.Load(ticketDir)
	s.BranchBaseSHA = base
	s.Save(ticketDir)
	promptPath := fakeClaudeRecordingPrompt(t, "VERDICT: PASS")

	var stdout, stderr bytes.Buffer
	if code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 0 {
		t.Fatalf("TicketReview: exit %d, stderr=%s", code, stderr.String())
	}
	prompt, _ := os.ReadFile(promptPath)
	for _, m := range []string{"FIRST-CHANGE-MARKER", "SECOND-CHANGE-MARKER"} {
		if !bytes.Contains(prompt, []byte(m)) {
			t.Errorf("reviewer prompt lacks %s from the branch diff", m)
		}
	}
}

// The reviewer must see the gate's evidence for this head: it may only
// pass "a gate it saw run", so the gate runs and records first.
func TestTicketReviewRunsGateBeforeReviewer(t *testing.T) {
	repo, _, ticketID := setUpTicketForStepTest(t)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "reviewer.md"), []byte("# reviewer\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ngate:\n  commands:\n    - \"true\"\n"), 0o644)
	promptPath := fakeClaudeRecordingPrompt(t, "VERDICT: PASS")
	var stdout, stderr bytes.Buffer
	if code := TicketReview([]string{"--repo", repo, "--ticket", ticketID}, &stdout, &stderr); code != 0 {
		t.Fatalf("TicketReview: exit %d, stderr=%s", code, stderr.String())
	}
	prompt, _ := os.ReadFile(promptPath)
	if !bytes.Contains(prompt, []byte("head=")) || !bytes.Contains(prompt, []byte("gate evidence: passed=true")) {
		t.Errorf("reviewer prompt lacks the gate evidence for this head")
	}
}
