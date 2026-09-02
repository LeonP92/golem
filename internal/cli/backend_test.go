package cli

import (
	"testing"

	"github.com/leonpham/golem/internal/agentrunner"
	"github.com/leonpham/golem/internal/config"
)

func TestNewRunnerReturnsClaudeCodeForClaudeCodeBackend(t *testing.T) {
	cfg := &config.Config{Backend: "claude-code"}
	runner, err := NewRunner(cfg, "/repo/worktree")
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	if _, ok := runner.(agentrunner.ClaudeCode); !ok {
		t.Fatalf("got %T, want agentrunner.ClaudeCode", runner)
	}
}

func TestNewRunnerRejectsUnknownBackend(t *testing.T) {
	cfg := &config.Config{Backend: "not-a-real-backend"}
	if _, err := NewRunner(cfg, "/repo/worktree"); err == nil {
		t.Fatal("expected an error for an unsupported backend")
	}
}
