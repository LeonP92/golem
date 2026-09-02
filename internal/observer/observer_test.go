package observer

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/leonpham/golem/internal/agentrunner"
	"github.com/leonpham/golem/internal/blog"
)

func TestDispatchForCommitAppendsFindingFromResult(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")

	mock := agentrunner.NewMock()
	mock.ScriptResponse("convention-enforcer", agentrunner.Result{Output: "FINDING: missing error check", Model: "mock"})

	o := New(logPath, mock)
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

	o := New(logPath, mock)

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
	o := New(logPath, mock)

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
