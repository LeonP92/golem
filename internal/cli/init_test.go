package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitRequiresBackendFlag(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Init([]string{"--repo", dir}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit when --backend is omitted and not interactive")
	}
}

func TestInitCreatesGolemLayout(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Init([]string{"--repo", dir, "--backend", "claude-code"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Init failed: exit %d, stderr=%s", code, stderr.String())
	}

	golemDir := filepath.Join(dir, ".golem")
	for _, p := range []string{
		filepath.Join(golemDir, "config.yaml"),
		filepath.Join(golemDir, "roles", "developer.md"),
		filepath.Join(golemDir, "wiki"),
		filepath.Join(golemDir, ".gitignore"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	gitignore, err := os.ReadFile(filepath.Join(golemDir, ".gitignore"))
	if err != nil {
		t.Fatalf("ReadFile .gitignore: %v", err)
	}
	content := string(gitignore)
	for _, want := range []string{"index/", "tickets/"} {
		if !strings.Contains(content, want) {
			t.Errorf(".gitignore missing %q, got: %s", want, content)
		}
	}
}
