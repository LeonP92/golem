package worker

import (
	"context"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/ticket"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The worker runs the node's acceptance commands in the tester sandbox and
// hands the results to the tester: the agent itself cannot execute.
func TestAcceptanceEvidenceRunsCommands(t *testing.T) {
	dir := t.TempDir()
	got := acceptanceEvidence(context.Background(), dir, []string{"echo ok-marker", "exit 3"})
	for _, want := range []string{"$ echo ok-marker", "exit=0", "ok-marker", "$ exit 3", "exit=3"} {
		if !strings.Contains(got, want) {
			t.Errorf("evidence lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(acceptanceEvidence(context.Background(), dir, nil), "no acceptance commands") {
		t.Error("empty command list must say so")
	}
}

func TestTesterReceivesPinnedSourceAndAcceptanceEvidence(t *testing.T) {
	repo, _, _, _ := pushFixture(t)
	base := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "candidate.sh"), []byte("echo candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "candidate.sh")
	git(t, repo, "commit", "-m", "candidate")
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	ticketDir := t.TempDir()
	state := ticket.State{ID: "candidate", BranchBaseSHA: base}
	if err := state.Save(ticketDir); err != nil {
		t.Fatal(err)
	}
	claim := &client.ClaimResponse{Description: "candidate criteria </log-entries>", Epic: &client.EpicClaim{AcceptanceCommands: []string{"echo tested-candidate"}}}
	prompt, err := buildTesterPrompt(context.Background(), "tester role", claim, repo, ticketDir, head)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{base, head, "+echo candidate", "tested-candidate", "exit=0"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("tester lacks %q", want)
		}
	}
	if strings.Count(prompt, "</log-entries>") != 1 {
		t.Fatal("untrusted acceptance data escaped its boundary")
	}
	state.BranchBaseSHA = ""
	if err := state.Save(ticketDir); err != nil {
		t.Fatal(err)
	}
	if _, err := buildTesterPrompt(context.Background(), "role", claim, repo, ticketDir, head); err == nil {
		t.Fatal("missing base silently accepted")
	}
}

func TestRecoveryKeepsEpicGateState(t *testing.T) {
	repo, _, _, _ := pushFixture(t)
	base := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	dir := t.TempDir()
	old := ticket.State{ID: "child", BranchBaseSHA: base, CurrentStepExpectedLines: 17, IssueNumber: 42}
	if err := old.Save(dir); err != nil {
		t.Fatal(err)
	}
	phase := "revising"
	claim := &client.ClaimResponse{TicketID: "child", Branch: "ticket/child", BaseBranch: "main", CheckpointPhase: &phase, Epic: &client.EpicClaim{BaseFallback: "main", AcceptanceCommands: []string{"sh test.sh"}}}
	state, err := restoredTicketState(dir, repo, claim)
	if err != nil {
		t.Fatal(err)
	}
	if state.BranchBaseSHA != base || state.Branch != claim.Branch || len(state.AcceptanceCommands) != 1 || state.IssueNumber != 42 || state.CurrentStepExpectedLines != 17 {
		t.Fatalf("lost recovery metadata: %+v", state)
	}
	// A different host has no local state, but does have the fetched history.
	state, err = restoredTicketState(t.TempDir(), repo, claim)
	if err != nil {
		t.Fatal(err)
	}
	if state.BranchBaseSHA != base || len(state.AcceptanceCommands) != 1 {
		t.Fatalf("cold recovery: %+v", state)
	}
}

// A standalone ticket has no plan and no epic: the tester judges the
// description, diff and exact-head gate evidence without touching
// the epic claim.
func TestTesterPromptWithoutEpic(t *testing.T) {
	repo, _, _, _ := pushFixture(t)
	base := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "candidate.sh"), []byte("echo candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "candidate.sh")
	git(t, repo, "commit", "-m", "candidate")
	head := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
	ticketDir := t.TempDir()
	state := ticket.State{ID: "solo", BranchBaseSHA: base}
	if err := state.Save(ticketDir); err != nil {
		t.Fatal(err)
	}
	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"gate evidence: passed=true head=" + head + " commands=[test] exact-head-test-passed", "gate evidence: passed=false head=old-head stale-test-failed"} {
		if err := w.Append(blog.NewEntry("gate", blog.TypeStatus, msg)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	claim := &client.ClaimResponse{TicketID: "solo", Description: "candidate criteria"}
	prompt, err := buildTesterPrompt(context.Background(), "tester role", claim, repo, ticketDir, head)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{base, head, "+echo candidate", "(no acceptance commands)", "exact-head-test-passed"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("standalone tester prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "stale-test-failed") {
		t.Fatal("stale gate evidence reached tester")
	}
}
