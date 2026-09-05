package soul

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leonp92/golem/internal/blog"
)

func TestExtractCandidatesFindsDivergentResolution(t *testing.T) {
	entries := []blog.Entry{
		{Role: "convention-enforcer", Type: blog.TypeBlocker, ID: "b1", Message: "use a struct here"},
		{Role: "human", Type: blog.TypeResolved, InReplyTo: "b1", Message: "no, keep it as separate params — we never bundle fewer than 4 args here"},
		{Role: "developer", Type: blog.TypeStatus, Message: "committed step 2"},
	}
	candidates := ExtractCandidates(entries)
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates, want 1", len(candidates))
	}
	if candidates[0].Suggestion == "" {
		t.Error("expected a non-empty suggestion derived from the resolution")
	}
}

func TestExtractCandidatesIgnoresNonDivergentActivity(t *testing.T) {
	entries := []blog.Entry{
		{Role: "developer", Type: blog.TypeStatus, Message: "committed step 1"},
		{Role: "reviewer", Type: blog.TypeFinding, Message: "looks fine"},
	}
	candidates := ExtractCandidates(entries)
	if len(candidates) != 0 {
		t.Fatalf("got %d candidates, want 0 for a ticket with no human divergence", len(candidates))
	}
}

func TestExtractCandidatesIgnoresResolutionMatchingOriginal(t *testing.T) {
	entries := []blog.Entry{
		{Role: "convention-enforcer", Type: blog.TypeBlocker, ID: "b1", Message: "use a struct here"},
		{Role: "human", Type: blog.TypeResolved, InReplyTo: "b1", Message: "use a struct here"},
	}
	candidates := ExtractCandidates(entries)
	if len(candidates) != 0 {
		t.Fatalf("got %d candidates, want 0 — the human agreed with the role, nothing diverged", len(candidates))
	}
}

func TestExtractCandidatesIgnoresResolutionWithNoMatchingBlocker(t *testing.T) {
	entries := []blog.Entry{
		{Role: "human", Type: blog.TypeResolved, InReplyTo: "nonexistent-id", Message: "some unrelated note"},
	}
	candidates := ExtractCandidates(entries)
	if len(candidates) != 0 {
		t.Fatalf("got %d candidates, want 0 — a RESOLVED entry with no matching BLOCKER has nothing to compare against", len(candidates))
	}
}

func TestPromoteWritesFile(t *testing.T) {
	dir := t.TempDir()
	err := Promote(dir, "extend-over-duplicate.md", "when introducing a new function, check whether an existing function's parameters could be extended to cover it before duplicating logic")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "extend-over-duplicate.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "when introducing a new function, check whether an existing function's parameters could be extended to cover it before duplicating logic" {
		t.Errorf("file content = %q", data)
	}
}

func TestPromoteCreatesSoulDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "soul")
	if err := Promote(dir, "a.md", "a principle"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.md")); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}
