package agentrunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The CLI path runs an agent too, and it runs it on the same untrusted text.
//
// `golem issue sync` and `golem ticket new --from-issue N` pull a GitHub issue
// body into a local ticket, and `golem observer dispatch` / `golem ticket
// review` then hand that text to `claude --print` through RunAgent below. There
// is no approval gate on this path at all — the gate is the orchestrator's, and
// CLI mode does not have one. So the claim "the agent never sees golem's
// credentials" has to hold here or it is not a claim, it is a coincidence of
// which binary you happened to run.
//
// internal/shem/worker has the mirror of this file. The allow-list they share
// lives in internal/agentenv and is tested there.

// fakeClaude installs a `claude` on PATH that writes its own environment to a
// file and returns that path. The prompt arrives on stdin and is discarded.
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nenv > " + dump + "\ncat > /dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dump
}

func TestRunAgentGivesTheAgentAScopedEnvironment(t *testing.T) {
	dump := fakeClaude(t)
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_agent")
	t.Setenv("GOLEM_SHEM_API_KEY", "must-not-reach-the-agent")
	t.Setenv("ORCHESTRATOR_DB", "/data/orchestrator.db")
	t.Setenv("ANTHROPIC_API_KEY", "sk-the-agent-needs-this")

	runner := ClaudeCode{RepoRoot: t.TempDir()}
	if _, err := runner.RunAgent("developer", Context{RolePrompt: "do the thing"}); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read the agent's environment: %v", err)
	}
	seen := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

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
