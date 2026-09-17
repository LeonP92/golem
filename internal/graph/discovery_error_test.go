package graph_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/graph"
)

// Discover returned git's error bare, so a directory with no repository in it
// produced only
//
//	discovering modules: exit status 128
//
// 128 is git's code for "not a git repository", and the message saying so was
// already captured on the *exec.ExitError by cmd.Output() — it was simply
// never read. The same opaque line appeared in the shem's logs as a repo
// pre-flight warning, where it gave no hint that the repository was missing.
func TestDiscoverNamesAMissingRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("a directory with no repository says so", func(t *testing.T) {
		dir := t.TempDir()
		_, err := graph.Discover(dir, 0, nil, nil)
		if err == nil {
			t.Fatal("Discover succeeded in a directory with no repository")
		}
		msg := err.Error()
		if strings.Contains(msg, "exit status 128") && !strings.Contains(msg, "not a git repository") {
			t.Fatalf("still only reports the exit status: %v", msg)
		}
		for _, want := range []string{"not a git repository", dir} {
			if !strings.Contains(msg, want) {
				t.Errorf("error %q does not mention %q", msg, want)
			}
		}
	})

	t.Run("a real repository still works", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command("git", "init", "-q")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v: %s", err, out)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		modules, err := graph.Discover(dir, 0, nil, nil)
		if err != nil {
			t.Fatalf("Discover in a real repository: %v", err)
		}
		if len(modules) == 0 {
			t.Error("no modules discovered in a repository with one Go file")
		}
	})
}
