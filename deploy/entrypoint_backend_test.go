package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runEntrypointForBackend runs the entrypoint with GOLEM_BACKEND set and
// returns its output plus the chosen HOME, so tests can inspect what the
// backend's bootstrap wrote there.
func runEntrypointForBackend(t *testing.T, backend string, env map[string]string) (string, string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "deploy", "shem-entrypoint.sh"))
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	script := strings.Replace(string(src), `exec golem-shem "$@"`, `echo "shem: would start"`, 1)
	if strings.Contains(script, "exec golem-shem") {
		t.Fatal("the exec line was not replaced; the test would try to start a real shem")
	}
	home := t.TempDir()
	path := filepath.Join(t.TempDir(), "entrypoint.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "HOME="+home)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if backend != "" {
		cmd.Env = append(cmd.Env, "GOLEM_BACKEND="+backend)
	}
	out, _ := cmd.CombinedOutput()
	return string(out), home
}

// The default with GOLEM_BACKEND unset stays claude-code: existing
// deployments carry no such variable and their bootstrap must not change.
func TestEntrypointDefaultsToClaudeCode(t *testing.T) {
	out, home := runEntrypointForBackend(t, "", map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-test",
	})
	if !strings.Contains(out, "bootstrapped") {
		t.Errorf("the claude bootstrap did not run by default:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Errorf("no .claude.json written by default: %v", err)
	}
}

// Another backend must not get the claude bootstrap: a codex deployment
// writes codex config, not a .claude.json.
func TestEntrypointCodexSkipsClaudeBootstrap(t *testing.T) {
	out, home := runEntrypointForBackend(t, "codex", map[string]string{
		"CODEX_API_KEY": "sk-test",
	})
	if strings.Contains(out, "bootstrapped") {
		t.Errorf("the claude bootstrap ran under GOLEM_BACKEND=codex:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf(".claude.json exists under GOLEM_BACKEND=codex")
	}
	// The codex trust file is created even when no repos are mounted yet, so
	// the adapter's PrepareHost appends into a file that already exists.
	if _, err := os.Stat(filepath.Join(home, ".codex", "config.toml")); err != nil {
		t.Errorf("no codex config.toml prepared: %v", err)
	}
}

func TestEntrypointOpencodeSkipsClaudeBootstrap(t *testing.T) {
	out, home := runEntrypointForBackend(t, "opencode", map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant",
	})
	if strings.Contains(out, "bootstrapped") {
		t.Errorf("the claude bootstrap ran under GOLEM_BACKEND=opencode:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf(".claude.json exists under GOLEM_BACKEND=opencode")
	}
}

// An unknown backend is named loudly at start rather than surfacing as an
// unauthenticated agent later.
func TestEntrypointWarnsOnUnknownBackend(t *testing.T) {
	out, _ := runEntrypointForBackend(t, "bogus", nil)
	if !strings.Contains(out, "unknown GOLEM_BACKEND") {
		t.Errorf("no warning for an unknown backend:\n%s", out)
	}
}
