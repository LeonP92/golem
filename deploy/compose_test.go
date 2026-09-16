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

// TestComposePassesGitHubTokenToServicesThatNeedIt pins the documented
// install path: an operator sets GOLEM_GITHUB_TOKEN in .env exactly as
// .env.example instructs, and docker compose must deliver it to every
// container that reads it.
//
// This is a regression test for a shipped-but-unreachable feature. .env.example
// documented the variable and docker-compose.yml never referenced it, so the
// whole GitHub integration was dead for anyone following the README: the
// orchestrator logged "N repo(s) enabled but GOLEM_GITHUB_TOKEN is empty"
// about a value the operator had set. Nothing in the Go test suite could see
// that, because no Go code reads docker-compose.yml.
func TestComposePassesGitHubTokenToServicesThatNeedIt(t *testing.T) {
	var compose composeFile
	if err := yaml.Unmarshal([]byte(repoFile(t, "docker-compose.yml")), &compose); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}

	tests := []struct {
		service string
		why     string
	}{
		{
			service: "orchestrator",
			why:     "reads it via config.github.token_env to poll issues and drain the write outbox",
		},
		{
			service: "shem",
			why:     "uses it as an HTTPS git credential to push the ticket branch a pull request is opened from",
		},
	}

	for _, tc := range tests {
		t.Run(tc.service, func(t *testing.T) {
			svc, ok := compose.Services[tc.service]
			if !ok {
				t.Fatalf("docker-compose.yml has no %q service", tc.service)
			}
			if svc.EnvFile != nil {
				// An env_file would also deliver the variable, but it would
				// deliver every other secret in .env too. If someone adds one
				// deliberately, this test should be revisited rather than
				// silently satisfied.
				t.Fatalf("service %q gained an env_file; revisit this test before relying on it", tc.service)
			}
			for _, entry := range svc.Environment {
				if strings.HasPrefix(entry, "GOLEM_GITHUB_TOKEN=") {
					if !strings.Contains(entry, "${GOLEM_GITHUB_TOKEN") {
						t.Fatalf("service %q sets GOLEM_GITHUB_TOKEN to a literal (%q); it must interpolate from .env",
							tc.service, entry)
					}
					return
				}
			}
			t.Fatalf("service %q does not receive GOLEM_GITHUB_TOKEN, but %s.\nenvironment: %v",
				tc.service, tc.why, svc.Environment)
		})
	}
}

// TestEnvExampleDocumentsGitHubToken keeps the variable the compose file
// interpolates and the variable .env.example tells operators to fill in from
// drifting apart — the pair is the whole contract.
func TestEnvExampleDocumentsGitHubToken(t *testing.T) {
	env := repoFile(t, ".env.example")
	for _, line := range strings.Split(env, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "GOLEM_GITHUB_TOKEN=") {
			return
		}
	}
	t.Fatal(".env.example no longer declares GOLEM_GITHUB_TOKEN, but docker-compose.yml interpolates it")
}
