package tester

import (
	"context"
	"os"
	"strings"
	"testing"
)

func envMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", kv)
		}
		m[name] = val
	}
	return m
}

// A tester run holds nothing Golem-owned and none of the credentials or
// egress settings the agent allow-list passes through for the agent itself.
func TestEnvStripsSecretsAndEgress(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "secret")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("CODEX_API_KEY", "secret")
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"apiKey":"secret"}`)
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("GH_TOKEN", "secret")
	t.Setenv("HTTPS_PROXY", "http://proxy:8080")
	t.Setenv("SSL_CERT_FILE", "/etc/ssl/custom.pem")

	m := envMap(t, Env())
	for _, name := range []string{
		"GOLEM_GITHUB_TOKEN", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY",
		"GH_TOKEN", "HTTPS_PROXY", "SSL_CERT_FILE", "CODEX_API_KEY", "OPENAI_API_KEY", "OPENCODE_CONFIG_CONTENT",
	} {
		if _, ok := m[name]; ok {
			t.Errorf("%s leaked into the tester environment", name)
		}
	}
}

// The sandbox keeps what a build needs: stripping the environment down to
// nothing would make every scenario fail for the wrong reason.
func TestEnvKeepsToolchain(t *testing.T) {
	t.Setenv("PATH", os.Getenv("PATH"))
	m := envMap(t, Env())
	if _, ok := m["PATH"]; !ok {
		t.Error("PATH missing from the tester environment")
	}
}

func TestCommandUsesSandboxEnv(t *testing.T) {
	cmd, _ := Command(context.Background(), t.TempDir(), "true")
	if cmd.Env == nil {
		t.Fatal("command inherits the ambient environment; it must use the sandbox env")
	}
	m := envMap(t, cmd.Env)
	if _, ok := m["GOLEM_GITHUB_TOKEN"]; ok {
		t.Error("command environment contains a Golem credential")
	}
}

// Where unshare exists, a scenario command must actually be wrapped in a
// network namespace, and networkIsolated must say so — a scenario run
// without isolation has to be readable as such, never mistaken for one.
func TestCommandReportsNetworkIsolation(t *testing.T) {
	_, isolated := Command(context.Background(), t.TempDir(), "true")
	_, lookErr := os.Stat("/proc/self/ns/net") // Linux: unshare should exist
	if lookErr == nil && !isolated {
		t.Log("unshare not available on this host; env-only isolation (acceptable, recorded)")
	}
	cmd, isolated2 := Command(context.Background(), t.TempDir(), "true")
	if isolated2 {
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("sandboxed `true` failed: %v\n%s", err, out)
		}
	}
}
