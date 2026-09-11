package worker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/shem/worker"
)

func TestReconstructState_WritesFiles(t *testing.T) {
	dir := t.TempDir()
	ticketDir := filepath.Join(dir, ".golem", "tickets", "ticket-uuid-42")
	if err := os.MkdirAll(ticketDir, 0o755); err != nil {
		t.Fatal(err)
	}

	phase := "implement"
	sha := "abc123"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-42",
		Branch:          "ticket/42",
		RepoRemote:      "r",
		CheckpointPhase: &phase,
		CheckpointSHA:   &sha,
		LogEntries: []db.LogEntry{
			{SequenceNum: 1, EntryType: "STATUS", Message: `{"type":"STATUS","message":"done"}`},
		},
	}

	worktreePath := filepath.Join(ticketDir, "worktree")
	if err := worker.ReconstructState(ticketDir, worktreePath, claim); err != nil {
		t.Fatalf("ReconstructState: %v", err)
	}

	// Verify state.json contains the phase.
	data, err := os.ReadFile(filepath.Join(ticketDir, "state.json"))
	if err != nil {
		t.Fatalf("read state.json: %v", err)
	}
	if !strings.Contains(string(data), "implement") {
		t.Errorf("state.json missing phase: %s", data)
	}

	// Verify log.jsonl has exactly one line.
	logData, err := os.ReadFile(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("read log.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 log line, got %d", len(lines))
	}
}

// TestReconstructState_Idempotent verifies that calling ReconstructState twice
// on the same directory does not fail.
func TestReconstructState_Idempotent(t *testing.T) {
	dir := t.TempDir()
	ticketDir := filepath.Join(dir, ".golem", "tickets", "ticket-uuid-7")

	phase := "review"
	sha := "deadbeef"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-7",
		Branch:          "ticket/7",
		RepoRemote:      "r",
		CheckpointPhase: &phase,
		CheckpointSHA:   &sha,
		LogEntries:      nil,
	}

	worktreePath := filepath.Join(ticketDir, "worktree")
	if err := worker.ReconstructState(ticketDir, worktreePath, claim); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := worker.ReconstructState(ticketDir, worktreePath, claim); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

// TestReconstructState_EmptyLogs verifies that log.jsonl is created but empty
// when ClaimResponse.LogEntries is nil.
func TestReconstructState_EmptyLogs(t *testing.T) {
	dir := t.TempDir()
	ticketDir := filepath.Join(dir, ".golem", "tickets", "ticket-uuid-99")

	phase := "plan"
	sha := "f00d"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-99",
		Branch:          "ticket/99",
		RepoRemote:      "r",
		CheckpointPhase: &phase,
		CheckpointSHA:   &sha,
		LogEntries:      nil,
	}

	worktreePath := filepath.Join(ticketDir, "worktree")
	if err := worker.ReconstructState(ticketDir, worktreePath, claim); err != nil {
		t.Fatalf("ReconstructState: %v", err)
	}

	logData, err := os.ReadFile(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("read log.jsonl: %v", err)
	}
	if strings.TrimSpace(string(logData)) != "" {
		t.Errorf("expected empty log.jsonl, got: %q", logData)
	}
}

// TestRecoverTicket_UsesClaimBranch verifies that RecoverTicket creates the
// worktree on claim.Branch (the server-computed slug branch), not a
// re-derived "ticket/<id>" name.
func TestRecoverTicket_UsesClaimBranch(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")
	run("branch", "ticket/human-friendly-name-abcd1234")
	headSHA, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	sha := strings.TrimSpace(string(headSHA))

	phase := "implement"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-branch",
		Branch:          "ticket/human-friendly-name-abcd1234",
		RepoRemote:      "local/repo",
		CheckpointPhase: &phase,
		CheckpointSHA:   &sha,
	}
	cfg := &config.Config{
		Repos: []config.RepoConfig{
			{Path: repoPath, NormalizedRemote: "local/repo"},
		},
	}

	if err := worker.RecoverTicket(ctx, claim, cfg); err != nil {
		t.Fatalf("RecoverTicket: %v", err)
	}

	worktreePath := filepath.Join(repoPath, ".golem", "tickets", claim.TicketID, "worktree")
	branchCmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("rev-parse --abbrev-ref HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != claim.Branch {
		t.Errorf("worktree branch = %q, want %q", got, claim.Branch)
	}
}
