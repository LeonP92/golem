package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// writeState puts a local ticket state.json in place, as `golem ticket new`
// and `golem ticket advance` do.
func writeState(t *testing.T, dir, phase, sha string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := json.Marshal(map[string]string{"phase": phase, "sha": sha})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), body, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}
}

// An agent that stops without advancing the local ticket has not finished.
//
// Both work phases used to post "… complete — ready for review" before
// reading the local state, then post whatever phase that state held —
// toOrchestratorPhase passes anything but "review"/"closed" through — so a
// ticket whose agent stopped at the planning gate was moved BACKWARDS to
// "plan" under a log line claiming it was ready for review. "plan" is
// resumable, so every shem restart re-ran the whole implementation pass.
func TestFinishWorkPhase(t *testing.T) {
	cfg := &config.Config{NoPush: true}
	claim := &client.ClaimResponse{TicketID: "t-1", Branch: "ticket/t-1"}

	tests := []struct {
		name        string
		localPhase  string
		writeState  bool
		wantMention []string
	}{
		{
			name:        "the agent stopped at the planning gate",
			localPhase:  "plan",
			writeState:  true,
			wantMention: []string{"did not complete", `"plan"`},
		},
		{
			name:        "the agent never ran golem ticket review",
			localPhase:  "implement",
			writeState:  true,
			wantMention: []string{"did not complete", `"implement"`},
		},
		{
			name:        "no local state at all",
			writeState:  false,
			wantMention: []string{"did not complete", "no readable ticket state"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.writeState {
				writeState(t, dir, tt.localPhase, "")
			}
			// Every row here is a refusal, so no client is needed. Which
			// local phases count as FINISHED is pinned by
			// TestToOrchestratorPhase below, and the real success path by the
			// lifecycle tests in internal/e2e that have an orchestrator.
			err := finishWorkPhase(context.Background(), cfg, nil, claim, dir, "Implementation", claim.Branch)
			if err == nil {
				t.Fatalf("local phase %q was accepted as finished", tt.localPhase)
			}
			for _, want := range tt.wantMention {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// toOrchestratorPhase is what let a local phase become an orchestrator phase
// unchecked. Pinning it documents which local phases mean "finished" — the
// question finishWorkPhase now asks before moving anything.
func TestToOrchestratorPhase(t *testing.T) {
	finished := map[string]bool{
		"review": true, "closed": true,
		"plan": false, "implement": false, "brainstorm": false, "": false,
	}
	for local, want := range finished {
		got := toOrchestratorPhase(local) == "ready-for-review"
		if got != want {
			t.Errorf("toOrchestratorPhase(%q) reads as finished=%v, want %v", local, got, want)
		}
	}
}
