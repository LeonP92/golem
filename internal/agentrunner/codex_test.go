package agentrunner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexArgv(t *testing.T) {
	tests := []struct {
		name  string
		opts  Options
		model string
		want  string
	}{
		{"no model, no extra args", Options{}, "", "codex exec --sandbox workspace-write -"},
		{"model only", Options{}, "gpt-5.6-sol", "codex exec --sandbox workspace-write --model gpt-5.6-sol -"},
		{"extra args only", Options{ExtraArgs: []string{"--ephemeral"}}, "", "codex exec --sandbox workspace-write --ephemeral -"},
		{"model then extra args", Options{ExtraArgs: []string{"--ephemeral"}}, "gpt-5.6-sol",
			"codex exec --sandbox workspace-write --model gpt-5.6-sol --ephemeral -"},
		{"command override", Options{Command: "my-codex"}, "", "my-codex exec --sandbox workspace-write -"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, err := Codex{opts: tt.opts}.argv(tt.model)
			if err != nil {
				t.Fatalf("argv: %v", err)
			}
			if got := strings.Join(argv, " "); got != tt.want {
				t.Errorf("argv = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCodexRejectsInvalidModel(t *testing.T) {
	if _, err := (Codex{}).argv("bad model"); err == nil {
		t.Fatal("argv accepted a model id with a space")
	}
}

// The rules file is the translation of the claude adapter's allow/deny
// list: every denied command appears as a forbidden prefix rule, and the
// broad allows are present so ordinary build commands run.
func TestCodexRules(t *testing.T) {
	rules := codexRules()
	for _, want := range []string{
		`prefix_rule(pattern = ["git", "push", "--force"], decision = "forbidden")`,
		`prefix_rule(pattern = ["sudo"], decision = "forbidden")`,
		`prefix_rule(pattern = ["git"], decision = "allow")`,
		`prefix_rule(pattern = ["golem"], decision = "allow")`,
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules missing %q", want)
		}
	}
}

func TestCodexWorktreeSetupWritesRules(t *testing.T) {
	dir := t.TempDir()
	if err := (Codex{}).WorktreeSetup(dir); err != nil {
		t.Fatalf("WorktreeSetup: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".codex", "rules", "golem.rules"))
	if err != nil {
		t.Fatalf("read rules: %v", err)
	}
	if !strings.Contains(string(data), "prefix_rule") {
		t.Errorf("rules file has no prefix_rule: %s", data)
	}
}

// Trust is appended as a TOML table, once, and existing files keep their
// content.
func TestTrustCodexProject(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".codex", "config.toml")

	if err := trustCodexProject(configPath, "/repos/test"); err != nil {
		t.Fatalf("trustCodexProject: %v", err)
	}
	data, _ := os.ReadFile(configPath)
	if !strings.Contains(string(data), `[projects."/repos/test"]`) ||
		!strings.Contains(string(data), `trust_level = "trusted"`) {
		t.Errorf("trust entry missing: %s", data)
	}

	if err := trustCodexProject(configPath, "/repos/test"); err != nil {
		t.Fatalf("trustCodexProject (second call): %v", err)
	}
	data, _ = os.ReadFile(configPath)
	if n := strings.Count(string(data), `"/repos/test"`); n != 1 {
		t.Errorf("trust entry written %d times, want 1: %s", n, data)
	}

	// A config with existing content keeps it.
	if err := trustCodexProject(configPath, "/repos/other"); err != nil {
		t.Fatalf("trustCodexProject (second repo): %v", err)
	}
	data, _ = os.ReadFile(configPath)
	for _, want := range []string{`"/repos/test"`, `"/repos/other"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config lost %s: %s", want, data)
		}
	}
}
