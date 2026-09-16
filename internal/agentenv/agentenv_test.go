package agentenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestAgentEnv is the allow-list, stated as cases rather than as a list.
//
// The agent subprocess is the one place in Golem where untrusted text becomes
// executed behaviour: the issue description reaches a `claude --print` prompt,
// and a successful prompt injection has whatever the process has. Before this,
// it had everything Golem's own process had — including GOLEM_GITHUB_TOKEN,
// which round 2 started delivering to the shem container, and which
// internal/cli/issue.go turns into ready-made tooling (`golem issue sync`,
// `golem ticket new --from-issue N`) already on PATH.
func TestAgentEnv(t *testing.T) {
	tests := []struct {
		name    string
		parent  []string
		want    []string
		notWant []string
	}{
		{
			name:    "golem's own secrets are stripped",
			parent:  []string{"GOLEM_GITHUB_TOKEN=ghp_secret", "GOLEM_SHEM_API_KEY=k", "GOLEM_SHEM_NAME=n", "ORCHESTRATOR_DB=/data/o.db", "PATH=/usr/bin"},
			want:    []string{"PATH=/usr/bin"},
			notWant: []string{"GOLEM_GITHUB_TOKEN=ghp_secret", "GOLEM_SHEM_API_KEY=k", "GOLEM_SHEM_NAME=n", "ORCHESTRATOR_DB=/data/o.db"},
		},
		{
			name:    "the agent's own model credentials pass through",
			parent:  []string{"ANTHROPIC_API_KEY=sk-x", "CLAUDE_CODE_OAUTH_TOKEN=t", "HOME=/root", "PATH=/usr/bin"},
			want:    []string{"ANTHROPIC_API_KEY=sk-x", "CLAUDE_CODE_OAUTH_TOKEN=t", "HOME=/root", "PATH=/usr/bin"},
			notWant: nil,
		},
		{
			name:    "shell, locale, proxy and TLS settings pass through",
			parent:  []string{"SHELL=/bin/bash", "LANG=C.UTF-8", "LC_ALL=C", "HTTPS_PROXY=http://p:3128", "SSL_CERT_FILE=/c.pem", "TZ=UTC", "TMPDIR=/tmp"},
			want:    []string{"SHELL=/bin/bash", "LANG=C.UTF-8", "LC_ALL=C", "HTTPS_PROXY=http://p:3128", "SSL_CERT_FILE=/c.pem", "TZ=UTC", "TMPDIR=/tmp"},
			notWant: nil,
		},
		{
			name:    "the toolchains the shem image ships pass through",
			parent:  []string{"GOROOT=/usr/local/go", "GOPATH=/go", "GOFLAGS=-mod=mod", "NODE_OPTIONS=--max-old-space-size=4096", "npm_config_registry=https://r", "PYTHONPATH=/p", "VIRTUAL_ENV=/v"},
			want:    []string{"GOROOT=/usr/local/go", "GOPATH=/go", "GOFLAGS=-mod=mod", "NODE_OPTIONS=--max-old-space-size=4096", "npm_config_registry=https://r", "PYTHONPATH=/p", "VIRTUAL_ENV=/v"},
			notWant: nil,
		},
		{
			name:    "git identity passes through but the push credential does not",
			parent:  []string{"GIT_AUTHOR_NAME=Golem", "GIT_COMMITTER_EMAIL=g@l", "GOLEM_GITHUB_TOKEN=ghp_secret"},
			want:    []string{"GIT_AUTHOR_NAME=Golem", "GIT_COMMITTER_EMAIL=g@l"},
			notWant: []string{"GOLEM_GITHUB_TOKEN=ghp_secret"},
		},
		{
			name:    "an unrecognised variable is not passed",
			parent:  []string{"COMPANY_VAULT_TOKEN=hunter2", "PATH=/usr/bin"},
			want:    []string{"PATH=/usr/bin"},
			notWant: []string{"COMPANY_VAULT_TOKEN=hunter2"},
		},
		{
			name:    "the operator can widen the allow-list for a toolchain it misses",
			parent:  []string{"GOLEM_AGENT_ENV=BAZEL_REAL, CCACHE_DIR", "BAZEL_REAL=/b", "CCACHE_DIR=/c", "OTHER=x"},
			want:    []string{"BAZEL_REAL=/b", "CCACHE_DIR=/c"},
			notWant: []string{"OTHER=x", "GOLEM_AGENT_ENV=BAZEL_REAL, CCACHE_DIR"},
		},
		{
			name:    "the escape hatch cannot be used to put golem's namespace back",
			parent:  []string{"GOLEM_AGENT_ENV=GOLEM_GITHUB_TOKEN", "GOLEM_GITHUB_TOKEN=ghp_secret", "PATH=/usr/bin"},
			want:    []string{"PATH=/usr/bin"},
			notWant: []string{"GOLEM_GITHUB_TOKEN=ghp_secret"},
		},
		{
			name:    "a malformed entry with no separator is dropped rather than guessed at",
			parent:  []string{"JUSTANAME", "PATH=/usr/bin"},
			want:    []string{"PATH=/usr/bin"},
			notWant: []string{"JUSTANAME"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Filter(tt.parent)
			index := map[string]bool{}
			for _, kv := range got {
				index[kv] = true
			}
			for _, kv := range tt.want {
				if !index[kv] {
					t.Errorf("missing %q from the agent environment\ngot: %v", kv, got)
				}
			}
			for _, kv := range tt.notWant {
				if index[kv] {
					t.Errorf("leaked %q into the agent environment\ngot: %v", kv, got)
				}
			}
		})
	}
}

// TestAgentEnvStripsEveryVariableGolemItselfReads is the invariant behind the
// allow-list, checked against the source rather than against a hand-written
// list that would go stale. Every os.Getenv/os.LookupEnv literal in
// non-test code names a variable that is Golem's own configuration, and none
// of them is the agent's business.
//
// If someone adds a new one outside the GOLEM_ namespace, this fails and
// golemOwnedNames gets one more entry — which is the point: the alternative
// is discovering it in a postmortem.
//
// Test files are excluded because they read PATH and other process basics for
// their own setup, which says nothing about what Golem configures itself with.
// One production read is invisible to this scan by construction:
// cmd/orchestrator/main.go does os.Getenv(cfg.GitHub.TokenEnv), a name from
// the config file. The allow-list covers it anyway — an operator-chosen name
// is not on it — which is the argument for an allow-list over a deny list.
func TestAgentEnvStripsEveryVariableGolemItselfReads(t *testing.T) {
	names := golemEnvNamesFromSource(t)
	if len(names) < 4 {
		t.Fatalf("scanned the tree and found only %d env reads (%v); the scan is broken, not the code", len(names), names)
	}
	parent := make([]string, 0, len(names))
	for _, n := range names {
		parent = append(parent, n+"=secret-"+n)
	}
	for _, kv := range Filter(parent) {
		t.Errorf("agent environment carries %q, which golem itself reads", kv)
	}
}

// golemEnvNamesFromSource collects every string literal passed to os.Getenv or
// os.LookupEnv in the repository, test files included.
func golemEnvNamesFromSource(t *testing.T) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "os" {
				return true
			}
			if sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil || name == "" {
				return true
			}
			seen[name] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	return names
}
