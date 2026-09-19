package worker

import (
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/slug"
)

// The prompt tells the agent which branch its worktree is on, and the agent
// acts on that: it commits there, and the revise prompt explicitly instructs
// it to stay on "the existing branch".
//
// Both prompts built the name as "ticket/" + ticketID. No branch of that name
// exists. Orchestrator tickets are created on slug.Branch(title, id) —
// ticket/<slug>-<id[:8]> — by ui/handlers.go, api/tickets.go and
// ghsync/ingest.go alike, so the agent was told a branch name that is not the
// one it is standing on. The prompt has to carry the real branch rather than
// rebuild it from a rule that is no longer true.
func TestPrompts_CarryTheRealBranchName(t *testing.T) {
	const (
		ticketID = "2de16a96-33bc-40f1-a40e-3e2bd919a331"
		title    = "Share a workflow with the org"
	)
	branch := slug.Branch(title, ticketID)
	if branch == "ticket/"+ticketID {
		t.Fatal("fixture is degenerate: the real branch equals the old guess")
	}

	for _, c := range []struct {
		name   string
		prompt string
	}{
		{"implement", buildImplementPrompt(ticketID, branch, "d")},
		{"revise", buildRevisePrompt(ticketID, branch, "d", "fb")},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(c.prompt, "branch "+branch) {
				t.Errorf("prompt does not name the real branch %q", branch)
			}
			// The old guess must not appear at all: an agent given both
			// would have no way to tell which is authoritative.
			if strings.Contains(c.prompt, "branch ticket/"+ticketID) {
				t.Errorf("prompt still names the non-existent branch ticket/%s", ticketID)
			}
		})
	}
}
