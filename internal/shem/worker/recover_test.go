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

func TestRecoverTicketForRetryKeepsDirtyWorktree(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-m", "base")
	sha := runGit("rev-parse", "HEAD")
	branch := "ticket/retry"
	runGit("branch", branch)
	ticketDir := filepath.Join(repo, ".golem", "tickets", "retry-ticket")
	worktree := filepath.Join(ticketDir, "worktree")
	runGit("worktree", "add", worktree, branch)
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	phase := "needs-attention"
	claim := &client.ClaimResponse{TicketID: "retry-ticket", Branch: branch, RepoRemote: "test-remote",
		CheckpointPhase: &phase, CheckpointSHA: &sha}
	cfg := &config.Config{Repos: []config.RepoConfig{{Path: repo, Remote: repo, NormalizedRemote: claim.RepoRemote}}}
	if err := worker.RecoverTicketForRetry(context.Background(), claim, cfg, nil); err != nil {
		t.Fatalf("RecoverTicketForRetry: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(worktree, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "uncommitted work\n" {
		t.Errorf("retry discarded in-progress worktree edits: %q", got)
	}
}

func TestRecoverTicketForRetryRestoresDesignDocuments(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-m", "base")
	sha := runGit("rev-parse", "HEAD")
	branch := "ticket/retry-documents"
	runGit("branch", branch)
	ticketDir := filepath.Join(repo, ".golem", "tickets", "retry-documents")
	worktree := filepath.Join(ticketDir, "worktree")
	runGit("worktree", "add", worktree, branch)

	phase := "plan"
	claim := &client.ClaimResponse{TicketID: "retry-documents", Branch: branch, RepoRemote: "test-remote",
		CheckpointPhase: &phase, CheckpointSHA: &sha,
		LogEntries: []db.LogEntry{
			{EntryType: "SPEC", Message: "# Spec from orchestrator"},
			{EntryType: "PLAN", Message: "# Plan from orchestrator"},
		}}
	cfg := &config.Config{Repos: []config.RepoConfig{{Path: repo, Remote: repo, NormalizedRemote: claim.RepoRemote}}}
	if err := worker.RecoverTicketForRetry(context.Background(), claim, cfg, nil); err != nil {
		t.Fatalf("RecoverTicketForRetry: %v", err)
	}
	for name, want := range map[string]string{
		"spec.md": "# Spec from orchestrator",
		"plan.md": "# Plan from orchestrator",
	} {
		got, err := os.ReadFile(filepath.Join(ticketDir, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestRecoverTicketForRetryRefusesWrongBranchWithoutResetting(t *testing.T) {
	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init")
	runGit("config", "user.email", "test@example.com")
	runGit("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "tracked.txt")
	runGit("commit", "-m", "base")
	sha := runGit("rev-parse", "HEAD")
	branch := "ticket/retry"
	runGit("branch", branch)
	otherBranch := "ticket/other"
	runGit("branch", otherBranch)
	ticketDir := filepath.Join(repo, ".golem", "tickets", "retry-ticket")
	worktree := filepath.Join(ticketDir, "worktree")
	runGit("worktree", "add", worktree, otherBranch)
	if err := os.WriteFile(filepath.Join(worktree, "tracked.txt"), []byte("uncommitted work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	phase := "implement"
	claim := &client.ClaimResponse{TicketID: "retry-ticket", Branch: branch, RepoRemote: "test-remote",
		CheckpointPhase: &phase, CheckpointSHA: &sha}
	cfg := &config.Config{Repos: []config.RepoConfig{{Path: repo, Remote: repo, NormalizedRemote: claim.RepoRemote}}}
	err := worker.RecoverTicketForRetry(context.Background(), claim, cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to reset") {
		t.Fatalf("RecoverTicketForRetry error = %v, want refusal to reset wrong-branch worktree", err)
	}
	got, err := os.ReadFile(filepath.Join(worktree, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "uncommitted work\n" {
		t.Errorf("wrong-branch retry altered existing worktree edits: %q", got)
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

	if err := worker.RecoverTicket(ctx, claim, cfg, func(string) error { return nil }); err != nil {
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

// TestRestoreDesignDocuments_RestoresFromOrchestratorLog covers the reported
// gap: a resume from a plan checkpoint on a fresh shem has no plan.md or
// spec.md on disk, but the orchestrator holds the posted copies in the
// ticket's log, and the resume must put them back.
func TestRestoreDesignDocuments_RestoresFromOrchestratorLog(t *testing.T) {
	ticketDir := t.TempDir()
	phase := "plan"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-9",
		CheckpointPhase: &phase,
		LogEntries: []db.LogEntry{
			{SequenceNum: 1, EntryType: "STATUS", Message: "Brainstorm complete"},
			{SequenceNum: 2, EntryType: "SPEC", Message: "# Spec\n\nthe spec"},
			{SequenceNum: 3, EntryType: "PLAN", Message: "# Plan\n\nthe plan"},
		},
	}

	if err := worker.RestoreDesignDocuments(ticketDir, claim); err != nil {
		t.Fatalf("RestoreDesignDocuments: %v", err)
	}
	for name, want := range map[string]string{
		"spec.md": "# Spec\n\nthe spec",
		"plan.md": "# Plan\n\nthe plan",
	} {
		data, err := os.ReadFile(filepath.Join(ticketDir, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", name, data, want)
		}
	}
}

// TestRestoreDesignDocuments_LocalFileWins: a present local document is what
// validation ran against, so the resume must not overwrite it with the
// posted copy.
func TestRestoreDesignDocuments_LocalFileWins(t *testing.T) {
	ticketDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(ticketDir, "spec.md"), []byte("local spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "plan.md"), []byte("local plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	phase := "plan"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-10",
		CheckpointPhase: &phase,
		LogEntries: []db.LogEntry{
			{SequenceNum: 1, EntryType: "SPEC", Message: "posted spec"},
			{SequenceNum: 2, EntryType: "PLAN", Message: "posted plan"},
		},
	}

	if err := worker.RestoreDesignDocuments(ticketDir, claim); err != nil {
		t.Fatalf("RestoreDesignDocuments: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(ticketDir, "plan.md"))
	if string(data) != "local plan" {
		t.Errorf("plan.md = %q, want the local file kept", data)
	}
}

// TestRestoreDesignDocuments_MissingEverywhereFails: for a brainstorm or
// plan checkpoint, a document that exists nowhere means the next phase would
// run on nothing — the resume must fail loudly instead.
func TestRestoreDesignDocuments_MissingEverywhereFails(t *testing.T) {
	ticketDir := t.TempDir()
	phase := "plan"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-11",
		CheckpointPhase: &phase,
		LogEntries:      []db.LogEntry{{SequenceNum: 1, EntryType: "SPEC", Message: "# Spec"}},
	}

	err := worker.RestoreDesignDocuments(ticketDir, claim)
	if err == nil {
		t.Fatal("expected an error when plan.md exists nowhere")
	}
	if !strings.Contains(err.Error(), "requeue") {
		t.Errorf("error should name the recovery path, got: %v", err)
	}
}

func TestRestoreDesignDocuments_PlanRetryDoesNotRequireItsOutput(t *testing.T) {
	ticketDir := t.TempDir()
	checkpoint := "plan"
	claim := &client.ClaimResponse{
		CheckpointPhase: &checkpoint,
		RetryPhase:      "plan",
		LogEntries:      []db.LogEntry{{EntryType: "SPEC", Message: "# Spec"}},
	}
	if err := worker.RestoreDesignDocuments(ticketDir, claim); err != nil {
		t.Fatalf("RestoreDesignDocuments: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ticketDir, "spec.md")); err != nil {
		t.Errorf("spec.md should be restored as the plan phase input: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ticketDir, "plan.md")); !os.IsNotExist(err) {
		t.Errorf("plan.md is the retry phase output and should not be required: err=%v", err)
	}
}

// TestRestoreDesignDocuments_BrainstormCheckpointNeedsSpecOnly: a brainstorm
// checkpoint resumes at plan, which reads spec.md but not plan.md (the phase
// the resume is heading to writes it).
func TestRestoreDesignDocuments_BrainstormCheckpointNeedsSpecOnly(t *testing.T) {
	ticketDir := t.TempDir()
	phase := "brainstorm"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-12",
		CheckpointPhase: &phase,
		LogEntries:      []db.LogEntry{{SequenceNum: 1, EntryType: "SPEC", Message: "# Spec"}},
	}

	if err := worker.RestoreDesignDocuments(ticketDir, claim); err != nil {
		t.Fatalf("RestoreDesignDocuments: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ticketDir, "plan.md")); !os.IsNotExist(err) {
		t.Error("plan.md should not be required or written for a brainstorm checkpoint")
	}
}

// TestRestoreDesignDocuments_LaterCheckpointBestEffort: a crashed
// implementation resumes without the documents the same way it did before —
// restore what the orchestrator has, log the rest, never fail.
func TestRestoreDesignDocuments_LaterCheckpointBestEffort(t *testing.T) {
	ticketDir := t.TempDir()
	phase := "implement"
	claim := &client.ClaimResponse{
		TicketID:        "ticket-uuid-13",
		CheckpointPhase: &phase,
		LogEntries:      []db.LogEntry{{SequenceNum: 1, EntryType: "PLAN", Message: "# Plan"}},
	}

	if err := worker.RestoreDesignDocuments(ticketDir, claim); err != nil {
		t.Fatalf("best-effort restore must not fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ticketDir, "plan.md")); err != nil {
		t.Error("plan.md should have been restored from the log")
	}
	if _, err := os.Stat(filepath.Join(ticketDir, "spec.md")); !os.IsNotExist(err) {
		t.Error("spec.md is missing everywhere; best-effort leaves it absent")
	}
}
