package cli

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/models"
)

// observerFixtureRepo is a repo with a ticket, one committed change, the
// convention-enforcer role file, and the given .golem/config.yaml.
func observerFixtureRepo(t *testing.T, config string) (repo, ticketID, sha string) {
	t.Helper()
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	if err := os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"),
		[]byte("# role\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha = gitCommit(t, worktree, "feature.go", "package main\n", "add feature")
	return repo, ticketID, sha
}

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
			// .golem/config.yaml is agent-writable, so role_models is ignored here.
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
			repo, ticketID, sha := observerFixtureRepo(t, tt.config)
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
			repo, ticketID, sha := observerFixtureRepo(t, tt.config)
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
	repo, ticketID, sha := observerFixtureRepo(t,
		"backend: claude-code\nrole_models:\n  convention-enforcer: sonnet\n")
	doc := writeBackendDoc(t, "backend:\n  adapter: claude-code\n")
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

// A backend document's command and env reach the agent process.
func TestBackendDocumentCommandAndEnvReachTheAgent(t *testing.T) {
	skipWithoutShell(t)
	repo, ticketID, sha := observerFixtureRepo(t, "backend: claude-code\n")

	// A recording script standing in for the vendor CLI, named by `command`.
	dir := t.TempDir()
	envDump := filepath.Join(dir, "env.txt")
	script := filepath.Join(dir, "my-agent")
	body := "#!/bin/sh\nenv > " + envDump + "\ncat > /dev/null\necho 'FINDING: noted'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	// A name outside agentenv's allow-list: the operator declared it, so it
	// passes. GOLEM_ names are refused when the document loads.
	doc := writeBackendDoc(t, "backend:\n  adapter: claude-code\n  command: "+script+
		"\n  env:\n    MY_TOOL_MARKER: operator-set\n")

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID,
		"--role", "convention-enforcer", "--commit", sha, "--backend-config", doc}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch: exit %d, stderr=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("the document's command was never run: %v", err)
	}
	got := string(raw)
	if !strings.Contains(got, "MY_TOOL_MARKER=operator-set") {
		t.Errorf("the operator's env entry did not reach the agent:\n%s", got)
	}
	if strings.Contains(got, "GOLEM_SECRET") {
		t.Errorf("a GOLEM_-prefixed env entry reached the agent:\n%s", got)
	}
}

// The marker is absent from a run without the flag, so it comes from the
// document rather than the ambient environment.
func TestWithoutTheBackendDocumentTheOperatorEnvIsAbsent(t *testing.T) {
	skipWithoutShell(t)
	repo, ticketID, sha := observerFixtureRepo(t, "backend: claude-code\n")

	dir := t.TempDir()
	envDump := filepath.Join(dir, "env.txt")
	body := "#!/bin/sh\nenv > " + envDump + "\ncat > /dev/null\necho 'FINDING: noted'\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID,
		"--role", "convention-enforcer", "--commit", sha}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch: exit %d, stderr=%s", code, stderr.String())
	}
	raw, err := os.ReadFile(envDump)
	if err != nil {
		t.Fatalf("the agent was never run: %v", err)
	}
	if strings.Contains(string(raw), "MY_TOOL_MARKER") {
		t.Error("the marker reached a run with no backend document")
	}
}

// A bad backend or model flag fails the command instead of degrading to stub
// narratives or an early "nothing to review" exit.
func TestBadBackendFlagsFailFast(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".golem"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds := []struct {
		name string
		run  func([]string, io.Writer, io.Writer) int
		args []string
	}{
		{"graph build", GraphBuild, []string{"--repo", repo}},
		{"graph update", GraphUpdate, []string{"--repo", repo}},
		{"observer dispatch", ObserverDispatch, []string{"--repo", repo, "--ticket", "t-1", "--role", "reviewer", "--commit", "HEAD"}},
		{"ticket close", TicketClose, []string{"--repo", repo, "--ticket", "t-1"}},
		{"ticket validate", TicketValidate, []string{"--repo", repo, "--ticket", "t-1", "--stage", "spec"}},
	}
	bad := map[string][]string{
		"unknown backend adapter": {"--backend", "nonesuch"},
		"invalid model id":        {"--model", "-p"},
	}
	for _, c := range cmds {
		for want, flags := range bad {
			var stderr bytes.Buffer
			code := c.run(append(append([]string{}, c.args...), flags...), io.Discard, &stderr)
			if code == 0 || !strings.Contains(stderr.String(), want) {
				t.Errorf("%s %v: exit %d, stderr %q; want non-zero naming %q", c.name, flags, code, stderr.String(), want)
			}
		}
	}
}

// skipWithoutShell skips a test whose fake binary is a POSIX shell script.
func skipWithoutShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake binary is a POSIX shell script")
	}
}
