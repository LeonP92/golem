package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonpham/golem/internal/ticket"
)

func TestTicketsListsAllTicketsWithPhase(t *testing.T) {
	repo := t.TempDir()
	for _, id := range []string{"t1", "t2"} {
		s := ticket.New(id, "", false)
		s.Branch = "ticket/" + id
		if err := s.Save(filepath.Join(repo, ".golem", "tickets", id)); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	var stdout, stderr bytes.Buffer
	code := Tickets([]string{"--repo", repo}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Tickets failed: exit %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "t1") || !strings.Contains(out, "t2") {
		t.Errorf("expected both ticket ids in output, got: %s", out)
	}
	if !strings.Contains(out, "brainstorm") {
		t.Errorf("expected phase in output, got: %s", out)
	}
}

func TestTicketsHandlesNoTicketsDir(t *testing.T) {
	repo := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Tickets([]string{"--repo", repo}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Tickets should succeed with 0 tickets, got exit %d, stderr=%s", code, stderr.String())
	}
}
