package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWikiRebuildThenSearchFindsRankedMatch(t *testing.T) {
	repo := t.TempDir()
	wikiDir := filepath.Join(repo, ".golem", "wiki", "modules")
	os.MkdirAll(wikiDir, 0o755)
	os.WriteFile(filepath.Join(wikiDir, "phone.md"), []byte("validates a phone number and returns an error for invalid input"), 0o644)
	os.WriteFile(filepath.Join(wikiDir, "billing.md"), []byte("calculates a monthly invoice total from line items"), 0o644)

	var stdout, stderr bytes.Buffer
	if code := WikiRebuild([]string{"--repo", repo}, &stdout, &stderr); code != 0 {
		t.Fatalf("WikiRebuild failed: exit %d, stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, ".golem", "index", "vectors.db")); err != nil {
		t.Fatalf("expected index file to exist after rebuild: %v", err)
	}

	stdout.Reset()
	code := WikiSearch([]string{"--repo", repo, "check", "a", "phone", "number"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("WikiSearch failed: exit %d, stderr=%s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "phone.md") {
		t.Fatalf("expected phone.md to rank first, got output:\n%s", stdout.String())
	}
}

func TestWikiSearchRequiresQuery(t *testing.T) {
	repo := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := WikiSearch([]string{"--repo", repo}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for an empty query")
	}
}
