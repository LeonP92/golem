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

// TestInitEmitsTheGitHubFieldsThatAreRequired pins what the generated
// github: block contains.
//
// It used to contain `write: true` and nothing else — the one key no code
// path reads, while omitting the two that are required. Running
// `golem issue list` straight after `golem init` therefore failed with
// "github.repo must be set", and the generated config gave no hint that the
// key even existed.
func TestInitEmitsTheGitHubFieldsThatAreRequired(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Init([]string{"--repo", dir, "--backend", "claude-code"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Init failed: exit %d, stderr=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, ".golem", "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(raw)

	tests := []struct {
		key  string
		want string
		why  string
	}{
		{key: "repo", want: "\n  repo:", why: "required by every golem issue command; never inferred from the git remote"},
		{key: "label", want: "\n  label:", why: "the trigger label golem issue list filters on"},
		{key: "write", want: "\n  write:", why: "records which side owns GitHub write access"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if !strings.Contains(got, tc.want) {
				t.Errorf("generated config has no github.%s, but it is %s:\n%s", tc.key, tc.why, got)
			}
		})
	}

	// The block must also explain what `write` is, since nothing enforces it.
	if !strings.Contains(got, "no CLI command writes to GitHub") {
		t.Errorf("generated config does not say that github.write is not enforced:\n%s", got)
	}
}
