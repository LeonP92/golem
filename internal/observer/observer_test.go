package observer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
)

func TestDispatchForCommitAppendsFindingFromResult(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")

	mock := agentrunner.NewMock()
	mock.ScriptResponse("convention-enforcer", agentrunner.Result{Output: "FINDING: missing error check", Model: "mock"})

	o := New(logPath, mock, "")
	err := o.DispatchForCommit("convention-enforcer", "abc123", "diff content", "role prompt")
	if err != nil {
		t.Fatalf("DispatchForCommit: %v", err)
	}

	entries, err := blog.ReadAll(logPath)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != blog.TypeFinding {
		t.Fatalf("expected one FINDING entry, got %+v", entries)
	}
	if !strings.Contains(entries[0].Message, "missing error check") {
		t.Errorf("entry message = %q", entries[0].Message)
	}
}

func TestDispatchForCommitSkipsAlreadyClaimedSignal(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")
	mock := agentrunner.NewMock()
	mock.ScriptResponse("reviewer", agentrunner.Result{Output: "FINDING: ok", Model: "mock"})

	o := New(logPath, mock, "")

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = o.DispatchForCommit("reviewer", "sha1", "diff", "prompt")
		}(i)
	}
	wg.Wait()

	// Exactly one of the two concurrent dispatches should have actually
	// invoked the mock (which only has one scripted response); the other
	// must observe the claim and skip without erroring.
	entries, err := blog.ReadAll(logPath)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 entry from the race (claim must prevent duplicate answers), got %d", len(entries))
	}
}

func TestDispatchForCommitSkipsIfAlreadyLoggedFromPriorRun(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")

	// Simulate a prior observer run having already reviewed this commit.
	w, err := blog.NewWriter(logPath)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	prior := blog.NewEntry("convention-enforcer", blog.TypeFinding, "already reviewed")
	prior.CommitSHA = "sha1"
	w.Append(prior)
	w.Close()

	// A brand-new Observer has an empty in-memory claim map — it must
	// consult the log, not just memory, to avoid re-dispatching.
	mock := agentrunner.NewMock() // no scripted response: RunAgent would error if called
	o := New(logPath, mock, "")

	if err := o.DispatchForCommit("convention-enforcer", "sha1", "diff", "prompt"); err != nil {
		t.Fatalf("DispatchForCommit: %v", err)
	}

	entries, err := blog.ReadAll(logPath)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the prior entry to be untouched and no new one added, got %d entries", len(entries))
	}
}

// A silent observer still leaves evidence it ran: the gating reviewer
// fails any category it cannot see checked.
func TestDispatchForCommitRecordsSilenceAsNoFindings(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.jsonl")
	mock := agentrunner.NewMock()
	mock.ScriptResponse("convention-enforcer", agentrunner.Result{Output: "", Model: "mock"})
	o := New(logPath, mock, "")
	if err := o.DispatchForCommit("convention-enforcer", "abc123", "diff", "prompt"); err != nil {
		t.Fatal(err)
	}
	entries, _ := blog.ReadAll(logPath)
	if len(entries) != 1 || entries[0].Type != blog.TypeStatus || entries[0].CommitSHA != "abc123" ||
		!strings.Contains(entries[0].Message, "no findings") {
		t.Fatalf("entries = %+v", entries)
	}
	// And the same commit is not reviewed twice.
	if err := New(logPath, mock, "").DispatchForCommit("convention-enforcer", "abc123", "diff", "prompt"); err != nil {
		t.Fatal(err)
	}
	if entries, _ = blog.ReadAll(logPath); len(entries) != 1 {
		t.Fatalf("re-dispatch duplicated the entry: %d", len(entries))
	}
}

// The spec observer must receive the plan, not infer it from the diff.
type contextRunner struct {
	*agentrunner.Mock
	input agentrunner.Context
}

func (r *contextRunner) RunAgent(_ context.Context, _ string, in agentrunner.Context, _ string) (agentrunner.Result, error) {
	r.input = in
	return agentrunner.Result{}, nil
}
func TestDispatchSuppliesTicketEvidence(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")
	w, err := blog.NewWriter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(blog.NewEntry("worker", blog.TypeStatus, "Approved by a human")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"spec.md", "plan.md"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("expected "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &contextRunner{Mock: agentrunner.NewMock()}
	if err := New(logPath, runner, "").DispatchForCommit("spec-adherence", "sha", "the diff", "the role"); err != nil {
		t.Fatal(err)
	}
	prompt := agentrunner.BuildPrompt(runner.input)
	for _, evidence := range []string{"Approved by a human", "expected spec.md", "expected plan.md", "the diff"} {
		if !strings.Contains(prompt, evidence) {
			t.Errorf("missing ticket evidence %q", evidence)
		}
	}
}
