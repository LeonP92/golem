package gate

import (
	"strings"
	"testing"

	"github.com/leonpham/golem/internal/config"
)

func TestRunAllCommandsPass(t *testing.T) {
	dir := t.TempDir()
	cfg := config.GateConfig{Commands: []string{"true", "echo ok"}}
	result, err := Run(dir, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected Passed=true, output: %s", result.Output)
	}
}

func TestRunStopsAtFirstFailure(t *testing.T) {
	dir := t.TempDir()
	cfg := config.GateConfig{Commands: []string{"true", "false", "echo should-not-run"}}
	result, err := Run(dir, cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Passed {
		t.Fatal("expected Passed=false when a command fails")
	}
	if strings.Contains(result.Output, "should-not-run") {
		t.Error("gate runner should stop at first failure, not run subsequent commands")
	}
}
