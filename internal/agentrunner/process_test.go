package agentrunner

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Every adapter process gets the filtered environment, and an operator's env
// block cannot add golem's own variables to it.
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

// RunPhase through a fake vendor CLI hands the agent the filtered environment.
func TestRunPhaseGivesTheAgentAScopedEnvironment(t *testing.T) {
	skipWithoutShell(t)
	dir := t.TempDir()
	dump := filepath.Join(dir, "env.txt")
	script := "#!/bin/sh\nenv > " + dump + "\ncat > /dev/null\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOLEM_GITHUB_TOKEN", "ghp_must_not_reach_the_agent")
	t.Setenv("GOLEM_SHEM_API_KEY", "must-not-reach-the-agent")
	t.Setenv("ANTHROPIC_API_KEY", "sk-the-agent-needs-this")

	cc := ClaudeCode{}
	if err := cc.RunPhase(context.Background(), dir, "a prompt", io.Discard, ""); err != nil {
		t.Fatalf("RunPhase: %v", err)
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

// skipWithoutShell skips a test whose fake binary is a POSIX shell script.
func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a POSIX shell script")
	}
}

// A process that exits 0 while a child still holds its output open succeeded.
func TestRunAgentCmdAcceptsAnExitedProcessWithALingeringChild(t *testing.T) {
	skipWithoutShell(t)
	cmd := exec.Command("sh", "-c", "sleep 2 & exit 0")
	cmd.Stdout = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	if err := runAgentCmd(cmd); err != nil {
		t.Errorf("runAgentCmd = %v, want success", err)
	}
}
