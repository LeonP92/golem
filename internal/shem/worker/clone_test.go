package worker_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/shem/worker"
)

// unreachable is a well-formed https remote that cannot be cloned. Using it
// lets these tests distinguish the two outcomes that matter — "decided to
// clone" from "decided to skip" — without a network, and without widening
// isSafeRemote's allowlist (which exists to block ext:: command execution)
// just to make a test convenient.
const unreachable = "https://127.0.0.1:1/nope.git"

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// CloneIfMissing used to ask whether the DIRECTORY existed. On a container
// where /repos is a volume mount the directory always exists, so the clone
// was skipped, and the first git command several steps later died with
// "fatal: not a git repository" — naming neither the repository nor the
// reason.
func TestCloneIfMissing(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	t.Run("attempts a clone when nothing is there", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "repo")
		err := worker.CloneIfMissing(context.Background(), dst, unreachable)
		if err == nil {
			t.Fatal("returned nil: the clone was skipped, not attempted")
		}
		if strings.Contains(err.Error(), "not empty") {
			t.Fatalf("misread an absent directory as non-empty: %v", err)
		}
	})

	t.Run("attempts a clone into an existing empty directory", func(t *testing.T) {
		// The reported failure: the mount exists, so the old check skipped.
		dst := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(dst, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		err := worker.CloneIfMissing(context.Background(), dst, unreachable)
		if err == nil {
			t.Fatal("an existing empty directory was treated as already cloned")
		}
		if strings.Contains(err.Error(), "not empty") {
			t.Fatalf("an empty directory was reported as non-empty: %v", err)
		}
	})

	t.Run("leaves an existing repository alone", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "repo")
		gitInit(t, dst)
		marker := filepath.Join(dst, "local-work.txt")
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		// Unreachable remote on purpose: if this tried to clone it would
		// fail, so returning nil is proof it recognised the repository.
		if err := worker.CloneIfMissing(context.Background(), dst, unreachable); err != nil {
			t.Fatalf("an existing repository was not recognised: %v", err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Error("the existing checkout was disturbed")
		}
	})

	t.Run("reports a non-empty directory that is not a repository", func(t *testing.T) {
		// Exactly the state the operator hit: golem init had created .golem
		// inside a volume-mounted directory that was never cloned.
		dst := filepath.Join(t.TempDir(), "repo")
		if err := os.MkdirAll(filepath.Join(dst, ".golem"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		err := worker.CloneIfMissing(context.Background(), dst, unreachable)
		if err == nil {
			t.Fatal("accepted a non-empty non-repository; the failure would surface later as a git exit status")
		}
		for _, want := range []string{"not a git repository", "not empty", dst} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("refuses an unsafe remote scheme", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "repo")
		err := worker.CloneIfMissing(context.Background(), dst, "ext::sh -c whoami")
		if err == nil || !strings.Contains(err.Error(), "scheme not allowed") {
			t.Errorf("err = %v, want the ext:: remote refused", err)
		}
	})
}
