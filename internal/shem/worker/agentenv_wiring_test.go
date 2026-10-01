package worker

import (
	"os/exec"
	"strings"
	"testing"
)

// asAgent runs every nested `golem` and repository command, and those invoke
// an agent in turn, so their environment must not carry golem's secrets.
func TestAsAgentDoesNotLeakGolemSecrets(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_agent")
	t.Setenv("GOLEM_SHEM_API_KEY", "must-not-reach-the-agent")
	t.Setenv("ORCHESTRATOR_DB", "/data/orchestrator.db")
	t.Setenv("ANTHROPIC_API_KEY", "sk-the-agent-needs-this")

	cmd := asAgent(exec.Command("golem", "ticket", "review"))
	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil, so the nested command inherits golem's entire environment")
	}
	var sawKey bool
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GOLEM_") || strings.HasPrefix(kv, "ORCHESTRATOR_DB=") {
			t.Errorf("the nested command environment carries %q", kv)
		}
		if kv == "ANTHROPIC_API_KEY=sk-the-agent-needs-this" {
			sawKey = true
		}
	}
	if !sawKey {
		t.Error("the nested command did not receive ANTHROPIC_API_KEY; it cannot reach a model")
	}
}
