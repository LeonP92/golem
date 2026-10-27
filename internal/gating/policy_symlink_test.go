package gating

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteThroughSymlinkOutOfTheWorktreeIsDenied(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if withinWorktree(root, filepath.Join(root, "link", "file")) {
		t.Fatal("a symlink pointing outside the worktree carried a write out")
	}
	if !withinWorktree(root, filepath.Join(root, "new", "file")) {
		t.Fatal("a not-yet-existing path inside the worktree was refused")
	}
}
