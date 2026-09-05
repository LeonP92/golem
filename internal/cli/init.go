package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/roles"
)

var supportedBackends = []string{"claude-code"}

const defaultConfigTemplate = `backend: %s
gate:
  commands: []
tool_policy:
  allow_network: []
  allow_worktree_only: true
ask_and_wait_timeout:
  %s: 5m
role_models: {}
`

const golemGitignore = "index/\ntickets/\n"

// Init requires an explicit --backend flag; there is no silent default
// (spec: Distribution & Ownership). A future interactive prompt path
// belongs to the terminal-attached CLI wrapper, not this testable core.
func Init(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	backend := fs.String("backend", "", "backend to use (required): "+strings.Join(supportedBackends, ", "))
	withGraph := fs.Bool("with-graph", false, "run 'golem graph build' after init")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *backend == "" {
		fmt.Fprintln(stderr, "error: --backend is required (e.g. --backend claude-code); golem init never picks a silent default")
		return 1
	}
	if !slices.Contains(supportedBackends, *backend) {
		fmt.Fprintf(stderr, "error: unsupported backend %q, supported: %s\n", *backend, strings.Join(supportedBackends, ", "))
		return 1
	}

	golemDir := filepath.Join(*repo, ".golem")
	if err := os.MkdirAll(golemDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "creating .golem: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Join(golemDir, "wiki", "modules"), 0o755); err != nil {
		fmt.Fprintf(stderr, "creating wiki dir: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Join(golemDir, "wiki", "soul"), 0o755); err != nil {
		fmt.Fprintf(stderr, "creating soul dir: %v\n", err)
		return 1
	}

	configPath := filepath.Join(golemDir, "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		content := fmt.Sprintf(defaultConfigTemplate, *backend, *backend)
		if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
			fmt.Fprintf(stderr, "writing config.yaml: %v\n", err)
			return 1
		}
	}

	if err := os.WriteFile(filepath.Join(golemDir, ".gitignore"), []byte(golemGitignore), 0o644); err != nil {
		fmt.Fprintf(stderr, "writing .gitignore: %v\n", err)
		return 1
	}

	if _, err := roles.Unpack(golemDir); err != nil {
		fmt.Fprintf(stderr, "unpacking role files: %v\n", err)
		return 1
	}

	if *backend == "claude-code" {
		roleContent := make(map[string]string)
		for _, name := range roles.RoleNames {
			data, err := os.ReadFile(filepath.Join(golemDir, "roles", name+".md"))
			if err != nil {
				fmt.Fprintf(stderr, "reading role file %s: %v\n", name, err)
				return 1
			}
			roleContent[name] = string(data)
		}
		if err := agentrunner.GenerateClaudeCodeArtifacts(*repo, roleContent); err != nil {
			fmt.Fprintf(stderr, "generating claude-code artifacts: %v\n", err)
			return 1
		}
		if err := agentrunner.GenerateClaudeCodeCommands(*repo); err != nil {
			fmt.Fprintf(stderr, "generating claude-code commands: %v\n", err)
			return 1
		}
	}

	fmt.Fprintf(stdout, "initialized .golem with backend %q\n", *backend)
	if *withGraph {
		fmt.Fprintln(stdout, "building code graph...")
		if code := GraphBuild([]string{"--repo", *repo}, stdout, stderr); code != 0 {
			return code
		}
	}
	return 0
}
