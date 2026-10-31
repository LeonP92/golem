package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// allBackendCLIs are the stub CLIs the harness installs by default: every
// backend the image can carry, so the CLI-presence check does not fire unless
// a test removes one on purpose.
var allBackendCLIs = []string{"claude", "codex", "opencode"}

// runEntrypointForBackend runs the entrypoint with GOLEM_BACKEND set and
// returns its output, exit code and the chosen HOME, so tests can inspect
// what the backend's bootstrap wrote there. stubs lists the backend CLIs
// that exist on PATH, standing in for the image's GOLEM_BACKENDS install.
func runEntrypointForBackend(t *testing.T, backend string, env map[string]string, stubs []string) (string, int, string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "deploy", "shem-entrypoint.sh"))
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	script := strings.Replace(string(src), `exec golem-shem "$@"`, `echo "shem: would start"`, 1)
	if strings.Contains(script, "exec golem-shem") {
		t.Fatal("the exec line was not replaced; the test would try to start a real shem")
	}

	bin := t.TempDir()
	for _, cli := range stubs {
		if err := os.WriteFile(filepath.Join(bin, cli), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write stub %s: %v", cli, err)
		}
	}

	home := t.TempDir()
	path := filepath.Join(t.TempDir(), "entrypoint.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	cmd := exec.Command("sh", path)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			kv = "PATH=" + bin + ":" + strings.TrimPrefix(kv, "PATH=")
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, "HOME="+home)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if backend != "" {
		cmd.Env = append(cmd.Env, "GOLEM_BACKEND="+backend)
	}
	out, runErr := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if runErr != nil {
		t.Fatalf("run entrypoint: %v", runErr)
	}
	return string(out), code, home
}

// GOLEM_BACKEND is required container configuration: unset, the entrypoint
// used to silently prepare claude-code. Now it names the valid values and
// stops, so an operator who never chose a backend does not get one guessed.
func TestEntrypointRequiresBackend(t *testing.T) {
	out, code, home := runEntrypointForBackend(t, "", map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-test",
	}, allBackendCLIs)
	if code == 0 {
		t.Errorf("started without GOLEM_BACKEND:\n%s", out)
	}
	if !strings.Contains(out, "GOLEM_BACKEND is not set") {
		t.Errorf("the error does not name the missing variable:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Error("a backend bootstrap ran before the missing variable was reported")
	}
}

// An unknown backend stops the container rather than surfacing as an
// unauthenticated agent later.
func TestEntrypointRejectsUnknownBackend(t *testing.T) {
	out, code, _ := runEntrypointForBackend(t, "bogus", nil, allBackendCLIs)
	if code == 0 {
		t.Errorf("started with an unknown backend:\n%s", out)
	}
	if !strings.Contains(out, "unknown GOLEM_BACKEND") {
		t.Errorf("the error does not name the unknown backend:\n%s", out)
	}
}

// A backend whose CLI is not in the image fails its first ticket otherwise;
// the entrypoint names the missing GOLEM_BACKENDS entry at startup instead.
func TestEntrypointRejectsMissingCLI(t *testing.T) {
	out, code, _ := runEntrypointForBackend(t, "claude-code", map[string]string{
		"CLAUDE_CODE_OAUTH_TOKEN": "sk-test",
	}, []string{"codex", "opencode"})
	if code == 0 {
		t.Errorf("started without the claude CLI:\n%s", out)
	}
	if !strings.Contains(out, "GOLEM_BACKENDS") {
		t.Errorf("the error does not point at GOLEM_BACKENDS:\n%s", out)
	}
}

// A selected backend with no credentials cannot run a single phase; that is
// a startup error, not a warning.
func TestEntrypointRejectsMissingCredentials(t *testing.T) {
	for _, tc := range []struct {
		backend string
		want    string
	}{
		{"claude-code", "no claude-code credentials"},
		{"codex", "no codex credentials"},
		{"opencode", "no opencode credentials"},
	} {
		out, code, _ := runEntrypointForBackend(t, tc.backend, map[string]string{
			"GOLEM_CODEX_MOUNT":    t.TempDir(),
			"GOLEM_OPENCODE_MOUNT": t.TempDir(),
		}, allBackendCLIs)
		if code == 0 {
			t.Errorf("%s: started with no credentials:\n%s", tc.backend, out)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: wrong or missing error %q:\n%s", tc.backend, tc.want, out)
		}
		if strings.Contains(out, "would start") {
			t.Errorf("%s: reached the shem start with no credentials:\n%s", tc.backend, out)
		}
	}
}

// Another backend must not get the claude bootstrap: a codex deployment
// writes codex config, not a .claude.json.
func TestEntrypointCodexSkipsClaudeBootstrap(t *testing.T) {
	out, code, home := runEntrypointForBackend(t, "codex", map[string]string{
		"CODEX_API_KEY": "sk-test",
	}, allBackendCLIs)
	if code != 0 {
		t.Fatalf("codex with CODEX_API_KEY must start:\n%s", out)
	}
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
	out, code, home := runEntrypointForBackend(t, "opencode", map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant",
	}, allBackendCLIs)
	if code != 0 {
		t.Fatalf("opencode with ANTHROPIC_API_KEY must start:\n%s", out)
	}
	if strings.Contains(out, "bootstrapped") {
		t.Errorf("the claude bootstrap ran under GOLEM_BACKEND=opencode:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf(".claude.json exists under GOLEM_BACKEND=opencode")
	}
}

// The session mount is found by its contents, not by CODEX_HOME, which
// compose does not pass in; and the host's config is only read.
func TestEntrypointCodexCopiesMountedSession(t *testing.T) {
	mount := t.TempDir()
	for name, body := range map[string]string{"auth.json": "{}", "config.toml": "model = \"x\"\n"} {
		if err := os.WriteFile(filepath.Join(mount, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, code, home := runEntrypointForBackend(t, "codex", map[string]string{"GOLEM_CODEX_MOUNT": mount}, allBackendCLIs)
	if code != 0 {
		t.Fatalf("codex with a mounted session must start:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "auth.json")); err != nil {
		t.Errorf("auth.json not copied from the mount: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(mount, "config.toml")); string(got) != "model = \"x\"\n" {
		t.Errorf("the mounted host config was modified: %q", got)
	}
}

// Every documented codex auth route still starts the container.
func TestEntrypointCodexAuthRoutes(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"access token": {"CODEX_ACCESS_TOKEN": "tok"},
		"api key":      {"CODEX_API_KEY": "sk-test"},
		"openai key":   {"OPENAI_API_KEY": "sk-test"},
	} {
		env["GOLEM_CODEX_MOUNT"] = t.TempDir()
		out, code, _ := runEntrypointForBackend(t, "codex", env, allBackendCLIs)
		if code != 0 {
			t.Errorf("%s: a valid codex auth route was rejected:\n%s", name, out)
		}
	}
}

func TestEntrypointOpencodeCopiesMountedAuth(t *testing.T) {
	mount := t.TempDir()
	if err := os.WriteFile(filepath.Join(mount, "auth.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code, home := runEntrypointForBackend(t, "opencode", map[string]string{"GOLEM_OPENCODE_MOUNT": mount}, allBackendCLIs)
	if code != 0 {
		t.Fatalf("opencode with mounted auth must start:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "opencode", "auth.json")); err != nil {
		t.Errorf("auth.json not copied from the mount: %v\n%s", err, out)
	}
	// Inline provider config can carry its own key.
	out, code, _ = runEntrypointForBackend(t, "opencode", map[string]string{
		"GOLEM_OPENCODE_MOUNT": t.TempDir(), "OPENCODE_CONFIG_CONTENT": "{}",
	}, allBackendCLIs)
	if code != 0 {
		t.Errorf("OPENCODE_CONFIG_CONTENT not accepted:\n%s", out)
	}
}

// A backend that authenticates by a route the check does not know (cloud
// credentials, a gateway token) opts out explicitly and is told it did.
func TestEntrypointCredentialCheckOptOut(t *testing.T) {
	out, code, _ := runEntrypointForBackend(t, "claude-code", map[string]string{
		"GOLEM_SKIP_CREDENTIAL_CHECK": "1",
	}, allBackendCLIs)
	if code != 0 {
		t.Errorf("the opt-out did not start the container:\n%s", out)
	}
	if !strings.Contains(out, "GOLEM_SKIP_CREDENTIAL_CHECK=1") {
		t.Errorf("the opt-out was not announced:\n%s", out)
	}
}
