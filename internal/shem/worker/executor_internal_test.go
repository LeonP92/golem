package worker

import (
	"strings"
	"testing"
)

// TestBuildRevisePrompt verifies the revise prompt embeds the ticket ID,
// worktree path, and feedback verbatim, and does not restate the plan
// (the revise session is scoped to fixes, not a re-implementation).
func TestBuildRevisePrompt(t *testing.T) {
	prompt := buildRevisePrompt("ticket-123", "fix the thing", "the button label is wrong")

	if !strings.Contains(prompt, "ticket-123") {
		t.Error("expected prompt to contain the ticket ID")
	}
	if !strings.Contains(prompt, "the button label is wrong") {
		t.Error("expected prompt to contain the feedback verbatim")
	}
	if !strings.Contains(prompt, ".golem/tickets/ticket-123/worktree/") {
		t.Error("expected prompt to reference the existing worktree path")
	}
	if strings.Contains(prompt, "Plan: .golem/tickets") {
		t.Error("revise prompt must not restate the plan like buildImplementPrompt does")
	}
}
