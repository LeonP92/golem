package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runEntrypoint runs the shem entrypoint against a throwaway HOME, with the
// final `exec golem-shem` replaced so it returns instead of starting a shem.
// Returns its combined output and the contents of $HOME/.claude.json, or ""
// when that file does not exist.
func runEntrypoint(t *testing.T, env map[string]string, seedBackup string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	src, err := os.ReadFile(filepath.Join("..", "deploy", "shem-entrypoint.sh"))
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	script := strings.Replace(string(src), `exec golem-shem "$@"`, `echo "shem: would start"`, 1)
	if strings.Contains(script, "exec golem-shem") {
		t.Fatal("the exec line was not replaced; the test would try to start a real shem")
	}

	home := t.TempDir()
	if seedBackup != "" {
		backups := filepath.Join(home, ".claude", "backups")
		if err := os.MkdirAll(backups, 0o755); err != nil {
			t.Fatalf("mkdir backups: %v", err)
		}
		if err := os.WriteFile(filepath.Join(backups, ".claude.json.backup.1"),
			[]byte(seedBackup), 0o600); err != nil {
			t.Fatalf("seed backup: %v", err)
		}
	}

	path := filepath.Join(t.TempDir(), "entrypoint.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "HOME="+home)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, _ := cmd.CombinedOutput()

	var claudeJSON string
	if b, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
		claudeJSON = string(b)
	}
	return string(out), claudeJSON
}

// The backup restore is a CLAUDE_HOME affordance — its own comment says so —
// but the condition never checked CLAUDE_HOME.
//
// With CLAUDE_HOME unset, /root/.claude is the throwaway shem-claude volume,
// which keeps whatever an earlier run left in it. So switching from
// CLAUDE_HOME to a headless token restored a STALE session on the next start,
// and because the bootstrap below only runs when .claude.json is ABSENT, the
// token never got its clean config. A container with a perfectly good
// CLAUDE_CODE_OAUTH_TOKEN then reports "Not logged in".
func TestEntrypointAuthBootstrap(t *testing.T) {
	const staleSession = `{"oauthAccount":{"emailAddress":"stale@example.com"},"projects":{}}`

	t.Run("a token with no CLAUDE_HOME gets a clean bootstrap", func(t *testing.T) {
		out, cfg := runEntrypoint(t, map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "sk-test",
			"CLAUDE_HOME":             "",
		}, staleSession)

		if strings.Contains(out, "restored") {
			t.Errorf("a stale backup was restored over a configured token:\n%s", out)
		}
		if !strings.Contains(out, "bootstrapped") {
			t.Errorf("the headless bootstrap did not run:\n%s", out)
		}
		if strings.Contains(cfg, "stale@example.com") {
			t.Error("the stale session reached .claude.json, which is what causes \"Not logged in\"")
		}
		if !strings.Contains(cfg, "hasCompletedSetup") {
			t.Errorf(".claude.json is not the bootstrapped config: %s", cfg)
		}
	})

	t.Run("CLAUDE_HOME still gets its backup restored", func(t *testing.T) {
		// The affordance this block exists for, unchanged.
		out, cfg := runEntrypoint(t, map[string]string{
			"CLAUDE_HOME": "/some/host/path",
		}, staleSession)
		if !strings.Contains(out, "restored") {
			t.Errorf("CLAUDE_HOME no longer restores its backup:\n%s", out)
		}
		if !strings.Contains(cfg, "stale@example.com") {
			t.Error("the restored config is not the backup")
		}
	})

	t.Run("both set warns which one wins", func(t *testing.T) {
		out, _ := runEntrypoint(t, map[string]string{
			"CLAUDE_CODE_OAUTH_TOKEN": "sk-test",
			"CLAUDE_HOME":             "/some/host/path",
		}, staleSession)
		if !strings.Contains(out, "WARNING both CLAUDE_HOME and a headless auth variable") {
			t.Errorf("no warning when both auth modes are set:\n%s", out)
		}
	})

	t.Run("no auth at all bootstraps nothing", func(t *testing.T) {
		out, cfg := runEntrypoint(t, map[string]string{"CLAUDE_HOME": ""}, "")
		if strings.Contains(out, "bootstrapped") {
			t.Errorf("bootstrapped a config with no auth configured:\n%s", out)
		}
		if cfg != "" {
			t.Errorf("wrote .claude.json with no auth configured: %s", cfg)
		}
	})
}
