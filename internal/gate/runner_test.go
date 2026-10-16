package gate

import (
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/config"
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

func TestRunCountsExecutedCommands(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, config.GateConfig{Commands: []string{"true", "true"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CommandsRun != 2 {
		t.Errorf("CommandsRun = %d, want 2", res.CommandsRun)
	}
}

// An empty check list passes vacuously and reports zero commands run.
func TestRunEmptyCommandsPassVacuously(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, config.GateConfig{Commands: nil})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Passed {
		t.Error("empty commands should still pass the review gate (vacuous)")
	}
	if res.CommandsRun != 0 {
		t.Errorf("CommandsRun = %d, want 0", res.CommandsRun)
	}
}

func TestRunStopsAtFirstFailureAndCountsIt(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(dir, config.GateConfig{Commands: []string{"false", "true"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Passed {
		t.Error("failing command should fail the gate")
	}
	if res.CommandsRun != 1 {
		t.Errorf("CommandsRun = %d, want 1 (stop at first failure)", res.CommandsRun)
	}
}
