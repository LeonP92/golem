package worker_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
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
