package worker

import (
	"context"
	"strings"
	"testing"
)

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
