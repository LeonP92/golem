package agentrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fake `codex` on PATH proves the whole invocation path: argv shape,
// prompt on stdin, stdout captured as the result.
func TestCodexRunAgentThroughFakeCLI(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "recorded")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + ".argv\ncat > " + record + ".stdin\nprintf 'codex-answer'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := (Codex{}).RunAgent(context.Background(), "reviewer", Context{RolePrompt: "ROLE", Diff: "D"}, "gpt-5.6-sol")
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res.Output != "codex-answer" {
		t.Errorf("Output = %q", res.Output)
	}
	argv, _ := os.ReadFile(record + ".argv")
	if got := strings.Join(strings.Fields(string(argv)), " "); got != "exec --sandbox workspace-write --model gpt-5.6-sol -" {
		t.Errorf("argv = %q", got)
	}
	stdin, _ := os.ReadFile(record + ".stdin")
	if !strings.Contains(string(stdin), "ROLE") || !strings.Contains(string(stdin), "<diff>") {
		t.Errorf("prompt not on stdin: %q", stdin)
	}
}

// The same proof for `opencode run`.
func TestOpenCodeRunAgentThroughFakeCLI(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "recorded")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + ".argv\ncat > " + record + ".stdin\nprintf 'opencode-answer'\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := (OpenCode{}).RunAgent(context.Background(), "reviewer", Context{RolePrompt: "ROLE"}, "anthropic/claude-opus-4-5")
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if res.Output != "opencode-answer" {
		t.Errorf("Output = %q", res.Output)
	}
	argv, _ := os.ReadFile(record + ".argv")
	if got := strings.Join(strings.Fields(string(argv)), " "); got != "run --model anthropic/claude-opus-4-5" {
		t.Errorf("argv = %q", got)
	}
	stdin, _ := os.ReadFile(record + ".stdin")
	if !strings.Contains(string(stdin), "ROLE") {
		t.Errorf("prompt not on stdin: %q", stdin)
	}
}
