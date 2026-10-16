package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tester prompt quotes the repo's configured runbook, and a repo
// without one (or with an unreadable config) reads as "none recorded" —
// inference comes first, so a missing runbook never stops the tester.
func TestRunbookSection(t *testing.T) {
	dir := t.TempDir()
	if got := runbookSection(dir); !strings.Contains(got, "none recorded") {
		t.Fatalf("missing config: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".golem"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "backend: mock\ngithub:\n  label: golem\nrunbook:\n  start: make run\n  e2e: make e2e\n  ports: \"8080\"\n  seed_data: ./seed.sh\n"
	if err := os.WriteFile(filepath.Join(dir, ".golem", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runbookSection(dir)
	for _, want := range []string{"make run", "make e2e", "8080", "./seed.sh"} {
		if !strings.Contains(got, want) {
			t.Errorf("runbook missing %q: %q", want, got)
		}
	}
}

// The worker runs the node's acceptance commands in the tester sandbox and
// hands the results to the tester: the agent itself cannot execute.
func TestAcceptanceEvidenceRunsCommands(t *testing.T) {
	dir := t.TempDir()
	got := acceptanceEvidence(context.Background(), dir, []string{"echo ok-marker", "exit 3"})
	for _, want := range []string{"$ echo ok-marker", "exit=0", "ok-marker", "$ exit 3", "exit=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(acceptanceEvidence(context.Background(), dir, nil), "no acceptance commands") {
		t.Error("empty command list must say so")
	}
}
