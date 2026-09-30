package agentrunner

import (
	"context"
	"strings"
	"testing"
)

// The allow-list lives in internal/agentenv and is tested there. What this
// asserts is that every adapter process is actually built with it, and that an
// operator's own env block cannot widen it.
func TestAgentCmdStripsGolemSecretsFromEveryAdapterProcess(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_agent")
	t.Setenv("GOLEM_SHEM_API_KEY", "must-not-reach-the-agent")
	t.Setenv("ORCHESTRATOR_DB", "/data/orchestrator.db")

	opts := Options{Env: map[string]string{
		"GOLEM_SECRET":       "smuggled-through-the-backend-block",
		"ANTHROPIC_BASE_URL": "https://proxy.example",
	}}
	cmd := agentCmd(context.Background(), opts, t.TempDir(), "claude", "--print")
	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil, so the agent inherits golem's entire environment")
	}
	var sawOperatorVar bool
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GOLEM_") || strings.HasPrefix(kv, "ORCHESTRATOR_DB=") {
			t.Errorf("agent subprocess environment carries %q", kv)
		}
		if kv == "ANTHROPIC_BASE_URL=https://proxy.example" {
			sawOperatorVar = true
		}
	}
	if !sawOperatorVar {
		t.Error("the operator's own env entry did not reach the agent")
	}
}
