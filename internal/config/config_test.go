package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadValidConfig(t *testing.T) {
	path := writeTempConfig(t, `
backend: claude-code
gate:
  commands:
    - "go build ./..."
    - "go test ./..."
tool_policy:
  allow_network: []
  allow_worktree_only: true
ask_and_wait_timeout:
  claude-code: 5m
role_models:
  developer: claude-sonnet-5
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backend != "claude-code" {
		t.Errorf("Backend = %q, want claude-code", cfg.Backend)
	}
	if len(cfg.Gate.Commands) != 2 {
		t.Errorf("Gate.Commands = %v, want 2 entries", cfg.Gate.Commands)
	}
	if !cfg.ToolPolicy.AllowWorktreeOnly {
		t.Error("AllowWorktreeOnly should be true")
	}
	d, err := cfg.AskAndWaitTimeoutFor("claude-code")
	if err != nil {
		t.Fatalf("AskAndWaitTimeoutFor: %v", err)
	}
	if d != 5*time.Minute {
		t.Errorf("timeout = %v, want 5m", d)
	}
}

func TestLoadMissingBackendFails(t *testing.T) {
	path := writeTempConfig(t, `
gate:
  commands: []
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for missing backend, got nil")
	}
}

func TestGitHubBlockDefaultsToNoWrite(t *testing.T) {
	path := writeTempConfig(t, "backend: claude-code\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitHub.Write {
		t.Error("GitHub.Write defaults to true; it must default to false so an " +
			"orchestrator-managed repo has exactly one writer")
	}
	if cfg.GitHub.Label != "golem" {
		t.Errorf("GitHub.Label = %q, want golem", cfg.GitHub.Label)
	}
}

func TestGitHubBlockParsed(t *testing.T) {
	path := writeTempConfig(t, "backend: claude-code\ngithub:\n  repo: org/repo\n  label: agent\n  write: true\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitHub.Repo != "org/repo" || cfg.GitHub.Label != "agent" || !cfg.GitHub.Write {
		t.Errorf("GitHub = %+v, want {org/repo agent true}", cfg.GitHub)
	}
}

func TestAskAndWaitTimeoutForUnknownBackendFails(t *testing.T) {
	path := writeTempConfig(t, `
backend: claude-code
ask_and_wait_timeout:
  claude-code: 5m
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.AskAndWaitTimeoutFor("codex"); err == nil {
		t.Fatal("expected error for unconfigured backend timeout, got nil")
	}
}
