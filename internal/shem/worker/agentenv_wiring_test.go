package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The allow-list itself lives in internal/agentenv and is tested there. What
// this file asserts is that THIS package's agent exec site is actually built
// with it — which was the original defect: `grep -rn "cmd.Env" internal/ cmd/`
// returned nothing at all, so the allow-list could be perfect and still reach
// nothing. internal/agentrunner has the mirror of this file for the CLI path,
// and internal/gate/env_test.go covers the third executor of instructions
// Golem did not write: gate commands out of .golem/config.yaml.

// TestClaudePhaseCmdDoesNotLeakGolemSecrets closes the loop: the allow-list is
// only worth anything if the agent subprocess is actually built with it.
// Nothing else in this package sets cmd.Env, which was exactly the problem —
// `grep -rn "cmd.Env" internal/ cmd/` returned nothing at all.
func TestClaudePhaseCmdDoesNotLeakGolemSecrets(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_agent")
	t.Setenv("GOLEM_SHEM_API_KEY", "must-not-reach-the-agent")

	cmd := claudePhaseCmd(context.Background(), t.TempDir(), "prompt")
	if cmd.Env == nil {
		t.Fatal("cmd.Env is nil, so the agent inherits golem's entire environment")
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "GOLEM_") {
			t.Errorf("agent subprocess environment carries %q", kv)
		}
	}
}

// TestRunClaudePhaseGivesTheAgentAScopedEnvironment is the end-to-end form of
// the same claim, and the one that would have caught the original state: it
// puts a fake `claude` on PATH, runs the real runClaudePhase through it, and
// reads back the environment the agent process actually received.
func TestRunClaudePhaseGivesTheAgentAScopedEnvironment(t *testing.T) {
	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nenv > " + dump + "\ncat > /dev/null\n"
	fake := filepath.Join(dir, "claude")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_agent")
	t.Setenv("GOLEM_SHEM_API_KEY", "must-not-reach-the-agent")
	t.Setenv("ANTHROPIC_API_KEY", "sk-the-agent-needs-this")

	if err := runClaudePhase(context.Background(), dir, "a prompt"); err != nil {
		t.Fatalf("runClaudePhase: %v", err)
	}
	got, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read the agent's environment: %v", err)
	}
	seen := strings.Split(strings.TrimRight(string(got), "\n"), "\n")

	for _, kv := range seen {
		if strings.HasPrefix(kv, "GOLEM_") || strings.HasPrefix(kv, "ORCHESTRATOR_DB=") {
			t.Errorf("the agent process received %q", kv)
		}
	}
	var sawKey bool
	for _, kv := range seen {
		if kv == "ANTHROPIC_API_KEY=sk-the-agent-needs-this" {
			sawKey = true
		}
	}
	if !sawKey {
		t.Errorf("the agent process did not receive ANTHROPIC_API_KEY; it cannot reach a model\ngot: %v", seen)
	}
}
