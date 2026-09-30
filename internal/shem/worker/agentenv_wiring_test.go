package worker

import (
	"os/exec"
	"strings"
	"testing"
)

// The allow-list itself lives in internal/agentenv and is tested there. What
// this file asserts is that THIS package's exec sites are actually built with
// it — which was the original defect: `grep -rn "cmd.Env" internal/ cmd/`
// returned nothing at all, so the allow-list could be perfect and still reach
// nothing. internal/agentrunner has the mirror of this for the adapter's own
// agent process, and internal/gate/env_test.go covers the third executor of
// instructions Golem did not write: gate commands out of .golem/config.yaml.

// asAgent is what every nested `golem` and repository command in this package
// runs through, and those commands invoke an agent in turn.
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
