package worker

import (
	"os/exec"
	"strings"
	"testing"
)

// A nested Golem CLI needs the allow-list policy to run gates, but neither it
// nor repository commands may receive Golem's credentials.
func TestAsGolemCLICarriesGatePolicyWithoutGolemSecrets(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_agent")
	t.Setenv("GOLEM_SHEM_API_KEY", "must-not-reach-the-agent")
	t.Setenv("GOLEM_AGENT_ENV", "APP_TEST_DATABASE_URL")
	t.Setenv("ORCHESTRATOR_DB", "/data/orchestrator.db")
	t.Setenv("ANTHROPIC_API_KEY", "sk-the-agent-needs-this")
	t.Setenv("APP_TEST_DATABASE_URL", "postgres://app@localhost/app_test")

	cmd := asGolemCLI(exec.Command("golem", "ticket", "review"))
	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil, so the nested command inherits golem's entire environment")
	}
	var sawKey bool
	var sawPolicy, sawDatabaseURL bool
	for _, kv := range cmd.Env {
		if (strings.HasPrefix(kv, "GOLEM_") && kv != "GOLEM_AGENT_ENV=APP_TEST_DATABASE_URL") ||
			strings.HasPrefix(kv, "ORCHESTRATOR_DB=") {
			t.Errorf("the nested command environment carries %q", kv)
		}
		if kv == "GOLEM_AGENT_ENV=APP_TEST_DATABASE_URL" {
			sawPolicy = true
		}
		if kv == "APP_TEST_DATABASE_URL=postgres://app@localhost/app_test" {
			sawDatabaseURL = true
		}
		if kv == "ANTHROPIC_API_KEY=sk-the-agent-needs-this" {
			sawKey = true
		}
	}
	if !sawKey {
		t.Error("the nested command did not receive ANTHROPIC_API_KEY; it cannot reach a model")
	}
	if !sawPolicy || !sawDatabaseURL {
		t.Errorf("nested Golem CLI is missing its safe gate env policy or explicitly allowed database URL: policy=%v database_url=%v", sawPolicy, sawDatabaseURL)
	}

	gitCmd := asAgent(exec.Command("git", "status"))
	for _, kv := range gitCmd.Env {
		if strings.HasPrefix(kv, "GOLEM_") {
			t.Errorf("repository command environment carries %q", kv)
		}
	}
}
