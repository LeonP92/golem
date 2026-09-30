package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/models"
)

func parseModelFlags(t *testing.T, args ...string) modelFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	mf := addModelFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return mf
}

func TestModelFlagsResolvePrecedence(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		cfg     *config.Config
		want    string
		wantErr bool
	}{
		{
			name: "--model wins",
			args: []string{"--model", "opus"},
			cfg:  &config.Config{RoleModels: map[string]string{"reviewer": "haiku", "review": "sonnet"}},
			want: "opus",
		},
		{
			name: "role_models keyed by role",
			cfg:  &config.Config{RoleModels: map[string]string{"reviewer": "haiku"}},
			want: "haiku",
		},
		{
			name: "role_models keyed by stage",
			cfg:  &config.Config{RoleModels: map[string]string{"review": "sonnet"}},
			want: "sonnet",
		},
		{
			name: "the role key wins over the stage key",
			cfg:  &config.Config{RoleModels: map[string]string{"reviewer": "haiku", "review": "sonnet"}},
			want: "haiku",
		},
		{
			name: "nothing configured",
			cfg:  &config.Config{},
			want: "",
		},
		{
			// The document comes from the shem; .golem/config.yaml is
			// agent-writable, so it must not reach that path.
			name: "backend-config ignores role_models",
			args: []string{"--backend-config", "/tmp/backend.yaml"},
			cfg:  &config.Config{RoleModels: map[string]string{"reviewer": "haiku"}},
			want: "",
		},
		{
			name:    "an invalid --model is an error",
			args:    []string{"--model", "-p"},
			cfg:     &config.Config{},
			wantErr: true,
		},
		{
			name:    "an invalid role_models value is an error",
			cfg:     &config.Config{RoleModels: map[string]string{"reviewer": "a b"}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mf := parseModelFlags(t, tt.args...)
			got, err := mf.resolve(tt.cfg, "reviewer", models.StageReview)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolve = %q, want %q", got, tt.want)
			}
		})
	}
}

// fakeClaudeRecordingArgv puts a `claude` on PATH that appends its own argv to
// a file and prints output, and returns that file's path.
func fakeClaudeRecordingArgv(t *testing.T, output string) string {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv.txt")
	src := `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	f, _ := os.OpenFile(` + "`" + argvPath + "`" + `, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	fmt.Fprintln(f, strings.Join(os.Args[1:], " "))
	f.Close()
	fmt.Println(` + "`" + output + "`" + `)
}
`
	buildFakeClaude(t, dir, src)
	return argvPath
}

func TestModelReachesTheAgentProcess(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		config  string
		wantArg string
	}{
		{name: "--model", args: []string{"--model", "opus"}, config: "backend: claude-code\n", wantArg: "--model opus"},
		{
			name:    "role_models by role",
			config:  "backend: claude-code\nrole_models:\n  convention-enforcer: sonnet\n",
			wantArg: "--model sonnet",
		},
		{
			name:    "role_models by stage",
			config:  "backend: claude-code\nrole_models:\n  observe: haiku\n",
			wantArg: "--model haiku",
		},
		{name: "no selection", config: "backend: claude-code\n", wantArg: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, worktree, ticketID := setUpTicketForStepTest(t)
			if err := os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte(tt.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sha := gitCommit(t, worktree, "feature.go", "package main\n", "add feature")
			argvPath := fakeClaudeRecordingArgv(t, "FINDING: noted")

			var stdout, stderr bytes.Buffer
			args := append([]string{"--repo", repo, "--ticket", ticketID,
				"--role", "convention-enforcer", "--commit", sha}, tt.args...)
			if code := ObserverDispatch(args, &stdout, &stderr); code != 0 {
				t.Fatalf("ObserverDispatch: exit %d, stderr=%s", code, stderr.String())
			}
			argv, err := os.ReadFile(argvPath)
			if err != nil {
				t.Fatalf("the agent was never run: %v", err)
			}
			if tt.wantArg == "" {
				if strings.Contains(string(argv), "--model") {
					t.Errorf("argv carries a model with none selected: %q", argv)
				}
				return
			}
			if !strings.Contains(string(argv), tt.wantArg) {
				t.Errorf("argv = %q, want it to contain %q", argv, tt.wantArg)
			}
		})
	}
}

func TestAnInvalidModelExitsNonZeroWithNoAgentRun(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		config string
		names  string
	}{
		{name: "--model -p", args: []string{"--model", "-p"}, config: "backend: claude-code\n", names: "-p"},
		{name: "--model with a space", args: []string{"--model", "a b"}, config: "backend: claude-code\n", names: "a b"},
		{
			name:   "an invalid role_models value",
			config: "backend: claude-code\nrole_models:\n  convention-enforcer: \"a b\"\n",
			names:  "a b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, worktree, ticketID := setUpTicketForStepTest(t)
			if err := os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte(tt.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			sha := gitCommit(t, worktree, "feature.go", "package main\n", "add feature")
			argvPath := fakeClaudeRecordingArgv(t, "FINDING: noted")

			var stdout, stderr bytes.Buffer
			args := append([]string{"--repo", repo, "--ticket", ticketID,
				"--role", "convention-enforcer", "--commit", sha}, tt.args...)
			if code := ObserverDispatch(args, &stdout, &stderr); code == 0 {
				t.Fatal("an invalid model id exited 0")
			}
			if !strings.Contains(stderr.String(), tt.names) {
				t.Errorf("stderr = %q, want it to name %q", stderr.String(), tt.names)
			}
			if _, err := os.Stat(argvPath); err == nil {
				t.Error("an agent process was started for an invalid model id")
			}
		})
	}
}

func TestBackendConfigKeepsTheRepoConfigOutOfTheShemPath(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	cfgBody := "backend: claude-code\nrole_models:\n  convention-enforcer: sonnet\n"
	if err := os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := writeBackendDoc(t, "backend:\n  adapter: claude-code\n")
	sha := gitCommit(t, worktree, "feature.go", "package main\n", "add feature")
	argvPath := fakeClaudeRecordingArgv(t, "FINDING: noted")

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID,
		"--role", "convention-enforcer", "--commit", sha, "--backend-config", doc}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch: exit %d, stderr=%s", code, stderr.String())
	}
	argv, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("the agent was never run: %v", err)
	}
	if strings.Contains(string(argv), "--model") {
		t.Errorf("the repo config's role_models reached the shem path: %q", argv)
	}
}
