package wiki

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllReadsMarkdownFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "architecture.md"), []byte("# Architecture"), 0o644)
	os.MkdirAll(filepath.Join(dir, "modules"), 0o755)
	os.WriteFile(filepath.Join(dir, "modules", "auth.md"), []byte("# Auth module"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644)

	docs, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2 (.md files only)", len(docs))
	}
}

func TestHashIsStableAndDistinguishesContent(t *testing.T) {
	h1 := Hash("hello")
	h2 := Hash("hello")
	h3 := Hash("world")
	if h1 != h2 {
		t.Error("Hash should be deterministic for identical content")
	}
	if h1 == h3 {
		t.Error("Hash should differ for different content")
	}
}

func TestIsStaleDetectsChangedContent(t *testing.T) {
	doc := Doc{Content: "new content"}
	oldHash := Hash("old content")
	if !IsStale(doc, oldHash) {
		t.Error("changed content should be stale")
	}
	if IsStale(doc, Hash("new content")) {
		t.Error("unchanged content should not be stale")
	}
}
