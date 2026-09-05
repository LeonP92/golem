package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/ticket"
)

func TestTicketResumeShowsPhaseAndLastLogEntry(t *testing.T) {
	repo := t.TempDir()
	ticketDir := filepath.Join(repo, ".golem", "tickets", "t1")
	s := ticket.New("t1", "", false)
	s.Phase = ticket.PhaseImplement
	if err := s.Save(ticketDir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	w.Append(blog.NewEntry("developer", blog.TypeStatus, "committed step 3"))
	w.Close()

	var stdout, stderr bytes.Buffer
	code := TicketResume([]string{"--repo", repo, "--id", "t1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("TicketResume failed: exit %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "implement") {
		t.Errorf("expected phase in output, got: %s", out)
	}
	if !strings.Contains(out, "committed step 3") {
		t.Errorf("expected last log entry in output, got: %s", out)
	}
}

func TestTicketResumeMissingTicketFails(t *testing.T) {
	repo := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := TicketResume([]string{"--repo", repo, "--id", "nonexistent"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for a nonexistent ticket")
	}
}
