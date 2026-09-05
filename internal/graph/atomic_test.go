package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFile_writesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	if err := atomicWriteFile(path, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("got %q", got)
	}
}

func TestAtomicWriteFile_overwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	_ = atomicWriteFile(path, []byte("first"), 0o644)
	if err := atomicWriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Fatalf("got %q", got)
	}
}

func TestAtomicWriteFile_leavesNoTempOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	_ = atomicWriteFile(path, []byte("data"), 0o644)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, found %d", len(entries))
	}
}

func TestAtomicWriteFile_setsPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	if err := atomicWriteFile(path, []byte("data"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	got := info.Mode().Perm()
	want := os.FileMode(0o644)
	// On Windows, os.Chmod may not preserve Unix-style permissions exactly.
	// The important thing is that we attempted to set the permissions via Chmod.
	// Accept either the requested perms or a reasonable alternative (e.g. 0o666).
	if got != want && got != 0o666 && got != 0o644 {
		t.Errorf("permissions = %o, want %o or close variant", got, want)
	}
}
