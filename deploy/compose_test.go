package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// composeFile is the subset of docker-compose.yml this test reasons about.
// Only the service environment blocks matter here; everything else is
// deliberately ignored so an unrelated compose change cannot break this test.
type composeFile struct {
	Services map[string]struct {
		Environment []string `yaml:"environment"`
		EnvFile     any      `yaml:"env_file"`
	} `yaml:"services"`
}

// repoFile reads a file from the repository root (one level above this
// package).
func repoFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// envValue returns the right-hand side of the first `NAME=...` entry in a
// service's environment block, and whether it was there at all.
func envValue(env []string, name string) (string, bool) {
	for _, entry := range env {
		if k, v, ok := strings.Cut(entry, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// service looks a service up and refuses to reason about one that has grown an
// env_file, which would deliver every secret in .env into the container and
// would satisfy any check here for the wrong reason.
func service(t *testing.T, compose composeFile, name string) []string {
	t.Helper()
	svc, ok := compose.Services[name]
	if !ok {
		t.Fatalf("docker-compose.yml has no %q service", name)
	}
	if svc.EnvFile != nil {
		t.Fatalf("service %q gained an env_file; revisit this test before relying on it", name)
	}
	return svc.Environment
}

func parseCompose(t *testing.T) composeFile {
	t.Helper()
	var compose composeFile
	if err := yaml.Unmarshal([]byte(repoFile(t, "docker-compose.yml")), &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	return compose
}

// TestComposeDeliversEachGitHubCredentialOnlyWhereItIsUsED pins the two halves
// of the credential contract, which are not the same variable.
//
// This is a regression test for a shipped-but-unreachable feature and, since
// re-review finding F2, for its overcorrection. .env.example documented
// GOLEM_GITHUB_TOKEN and docker-compose.yml never referenced it, so the whole
// GitHub integration was dead for anyone following the README. The fix then
// delivered that same repo-write PAT to BOTH services — including the shem,
// whose shipped configuration (no_push: true) cannot use it, and whose agent
// subprocess used to inherit the entire environment. A prompt injection in an
// approved issue therefore reached a credential rather than only a shell.
//
// So: the orchestrator gets GOLEM_GITHUB_TOKEN, because it always needs it.
// The shem gets its push credential only if the operator sets a SECOND,
// deliberately separate variable, which is the same moment they turn no_push
// off. The container-side name stays GOLEM_GITHUB_TOKEN so
// deploy/shem-entrypoint.sh's credential helper is unchanged.
func TestComposeDeliversEachGitHubCredentialOnlyWhereItIsUsed(t *testing.T) {
	compose := parseCompose(t)

	t.Run("orchestrator", func(t *testing.T) {
		env := service(t, compose, "orchestrator")
		value, ok := envValue(env, "GOLEM_GITHUB_TOKEN")
		if !ok {
			t.Fatalf("the orchestrator does not receive GOLEM_GITHUB_TOKEN, but it reads it via "+
				"config.github.token_env to poll issues and drain the write outbox.\nenvironment: %v", env)
		}
		if !strings.Contains(value, "${GOLEM_GITHUB_TOKEN") {
			t.Fatalf("the orchestrator sets GOLEM_GITHUB_TOKEN to %q; it must interpolate from .env", value)
		}
	})

	t.Run("shem", func(t *testing.T) {
		env := service(t, compose, "shem")
		value, ok := envValue(env, "GOLEM_GITHUB_TOKEN")
		if !ok {
			t.Fatalf("the shem no longer receives GOLEM_GITHUB_TOKEN at all; "+
				"deploy/shem-entrypoint.sh reads exactly that name to install the push "+
				"credential, so no_push: false can no longer open a pull request.\nenvironment: %v", env)
		}
		if strings.Contains(value, "${GOLEM_GITHUB_TOKEN") {
			t.Fatalf("the shem is handed the orchestrator's GOLEM_GITHUB_TOKEN (%q). "+
				"That ships a repo-write PAT into the container that runs the agent, on a "+
				"default configuration (no_push: true) that cannot use it. It must come from "+
				"a separate opt-in variable.", value)
		}
		if !strings.Contains(value, "${GOLEM_SHEM_GITHUB_TOKEN") {
			t.Fatalf("the shem's GOLEM_GITHUB_TOKEN is %q; it must interpolate from "+
				"GOLEM_SHEM_GITHUB_TOKEN, the opt-in push credential", value)
		}
		if !strings.Contains(value, ":-}") {
			t.Fatalf("the shem's GOLEM_GITHUB_TOKEN is %q; it must default to empty so an "+
				"operator who has not opted in ships no credential", value)
		}
	})
}

// TestEnvExampleDocumentsBothGitHubTokens keeps the variables the compose file
// interpolates and the variables .env.example tells operators to fill in from
// drifting apart — the pair is the whole contract.
func TestEnvExampleDocumentsBothGitHubTokens(t *testing.T) {
	env := repoFile(t, ".env.example")
	for _, name := range []string{"GOLEM_GITHUB_TOKEN", "GOLEM_SHEM_GITHUB_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			for _, line := range strings.Split(env, "\n") {
				line = strings.TrimSpace(line)
				line = strings.TrimPrefix(line, "# ")
				if strings.HasPrefix(line, name+"=") {
					return
				}
			}
			t.Fatalf(".env.example no longer declares %s, but docker-compose.yml interpolates it", name)
		})
	}
}
