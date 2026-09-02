package blog

import (
	"path/filepath"
	"testing"
)

func TestWriterAppendAndReadAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")

	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	if err := w.Append(NewEntry("developer", TypeStatus, "first")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Append(NewEntry("reviewer", TypeFinding, "second")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	entries, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Message != "first" || entries[1].Message != "second" {
		t.Errorf("entries out of order or wrong content: %+v", entries)
	}
}

func TestWriterAppendIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(n int) {
			w.Append(NewEntry("developer", TypeStatus, "concurrent"))
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 20; i++ {
		<-done
	}

	entries, err := ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 20 {
		t.Fatalf("got %d entries, want 20 (a race would corrupt/drop lines)", len(entries))
	}
}

func TestReadAllMissingFile(t *testing.T) {
	entries, err := ReadAll("/nonexistent/path/log.jsonl")
	if err != nil {
		t.Fatalf("ReadAll on missing file should return empty, not error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}
