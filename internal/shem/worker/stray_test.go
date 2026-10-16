package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdoptStrayTicketFileMovesASpecWrittenInTheWorktree(t *testing.T) {
	ticketDir := filepath.Join(t.TempDir(), "abc")
	stray := filepath.Join(ticketDir, "worktree", ".golem", "tickets", "abc")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "log.jsonl"), []byte("{\"a\":1}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "spec.md"), []byte("spec"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "log.jsonl"), []byte("{\"b\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptStrayTicketFile(ticketDir, "spec.md")

	if got, _ := os.ReadFile(filepath.Join(ticketDir, "spec.md")); string(got) != "spec" {
		t.Errorf("spec.md = %q, want it adopted", got)
	}
	if got, _ := os.ReadFile(filepath.Join(ticketDir, "log.jsonl")); string(got) != "{\"a\":1}\n{\"b\":2}\n" {
		t.Errorf("log.jsonl = %q, want the stray entries appended", got)
	}
	if _, err := os.Stat(filepath.Join(stray, "spec.md")); !os.IsNotExist(err) {
		t.Error("stray spec.md was not removed")
	}
}

func TestAdoptStrayTicketFileKeepsAnExistingSpec(t *testing.T) {
	ticketDir := filepath.Join(t.TempDir(), "abc")
	stray := filepath.Join(ticketDir, "worktree", ".golem", "tickets", "abc")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "spec.md"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "spec.md"), []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(stray, "spec.md"), old, old); err != nil {
		t.Fatal(err)
	}

	adoptStrayTicketFile(ticketDir, "spec.md")

	if got, _ := os.ReadFile(filepath.Join(ticketDir, "spec.md")); string(got) != "real" {
		t.Errorf("spec.md = %q, want the existing file kept", got)
	}
}

func TestAdoptStrayTicketFileReplacesAnOlderSpec(t *testing.T) {
	ticketDir := filepath.Join(t.TempDir(), "abc")
	stray := filepath.Join(ticketDir, "worktree", ".golem", "tickets", "abc")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketDir, "spec.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(ticketDir, "spec.md"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "spec.md"), []byte("revised"), 0o644); err != nil {
		t.Fatal(err)
	}

	adoptStrayTicketFile(ticketDir, "spec.md")

	if got, _ := os.ReadFile(filepath.Join(ticketDir, "spec.md")); string(got) != "revised" {
		t.Errorf("spec.md = %q, want the newer stray spec", got)
	}
}
