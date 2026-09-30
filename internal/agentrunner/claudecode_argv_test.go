package agentrunner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeCodeArgv(t *testing.T) {
	tests := []struct {
		name  string
		opts  Options
		model string
		want  string
	}{
		{"no model, no extra args", Options{}, "", "claude --print"},
		{"model only", Options{}, "opus", "claude --print --model opus"},
		{"extra args only", Options{ExtraArgs: []string{"--add-dir", "/opt/x"}}, "", "claude --print --add-dir /opt/x"},
		{"model then extra args", Options{ExtraArgs: []string{"--add-dir", "/opt/x"}}, "opus",
			"claude --print --model opus --add-dir /opt/x"},
		{"command override", Options{Command: "my-claude"}, "", "my-claude --print"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, err := ClaudeCode{opts: tt.opts}.argv(tt.model)
			if err != nil {
				t.Fatalf("argv: %v", err)
			}
			if got := strings.Join(argv, " "); got != tt.want {
				t.Errorf("argv = %q, want %q", got, tt.want)
			}
		})
	}
}

// An invalid model id is refused rather than dropped: running the vendor
// default for a value the caller believed in hides a stale catalog.
func TestInvalidModelIsRefusedWithNoProcessStarted(t *testing.T) {
	// A `claude` on PATH that fails the test if it ever runs.
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cc := ClaudeCode{opts: Options{RepoRoot: dir}}
	for _, model := range []string{"-p", "--model", "a b", "a;b"} {
		if _, err := cc.RunAgent(context.Background(), "developer", Context{}, model); err == nil {
			t.Errorf("RunAgent accepted model %q", model)
		} else if !strings.Contains(err.Error(), model) {
			t.Errorf("RunAgent error for %q does not name the value: %v", model, err)
		}
		if err := cc.RunPhase(context.Background(), dir, "p", io.Discard, model); err == nil {
			t.Errorf("RunPhase accepted model %q", model)
		} else if !strings.Contains(err.Error(), model) {
			t.Errorf("RunPhase error for %q does not name the value: %v", model, err)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("an agent process was started for an invalid model id")
	}
}

func TestRunAgentReportsTheModelItWasGiven(t *testing.T) {
	skipWithoutShell(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\ncat >/dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cc := ClaudeCode{opts: Options{RepoRoot: dir}}

	for _, model := range []string{"", "sonnet"} {
		res, err := cc.RunAgent(context.Background(), "developer", Context{}, model)
		if err != nil {
			t.Fatalf("RunAgent(%q): %v", model, err)
		}
		if res.Model != model {
			t.Errorf("Result.Model = %q, want %q", res.Model, model)
		}
	}
}
