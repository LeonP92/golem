package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// These tests run a real policy-check binary and the generated native hooks,
// with no provider account or model call. The fake CLI is only tool transport.
func TestPolicyHooksLocalEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds local CLI")
	}
	bin := filepath.Join(t.TempDir(), golemName())
	build := exec.Command("go", "build", "-o", bin, "../../cmd/golem")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".golem"), 0755)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ntool_policy:\n  allow_worktree_only: true\n  allow_network: [allowed.test]\n"), 0644)
	s, err := prepareTools(repo, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	for _, tc := range []struct {
		event string
		allow bool
	}{
		{`{"tool_name":"Bash","tool_input":{"command":"git push origin feature"}}`, false},
		{`{"tool_name":"Bash","tool_input":{"command":"cp ../.env .env"}}`, false},
		{`{"tool_name":"Edit","tool_input":{"file_path":` + strconv.Quote(filepath.Join(filepath.Dir(repo), "outside.go")) + `}}`, false},
		{`{"tool_name":"Edit","tool_input":{"file_path":"main.go"}}`, true},
		{`{"tool_name":"Bash","tool_input":{"command":"curl https://blocked.test/"}}`, false},
		{`{"tool_name":"Bash","tool_input":{"command":"curl https://allowed.test/"}}`, true},
		{`invalid`, false},
	} {
		cmd := exec.Command("sh", "-c", s.command)
		cmd.Stdin = strings.NewReader(tc.event)
		out, err := cmd.CombinedOutput()
		if (err == nil) != tc.allow {
			t.Fatalf("%s => %s %v", tc.event, out, err)
		}
		if !tc.allow {
			if status, ok := err.(*exec.ExitError); !ok || status.ExitCode() != 2 {
				t.Fatalf("hook did not block: %v", err)
			}
		}
	}
	// Test the actual emitted OpenCode plugin in Node, including exception-based
	// denial before a simulated operation. This exercises serialization too.
	t.Run("opencode plugin", func(t *testing.T) {
		for _, tool := range []string{"node", "sh"} {
			if _, err := exec.LookPath(tool); err != nil {
				t.Skipf("%s is not installed; the OpenCode plugin test needs node and sh", tool)
			}
		}
		env, err := s.openCodeEnv("")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Plugin []string `json:"plugin"`
		}
		json.Unmarshal([]byte(strings.TrimPrefix(env[0], "OPENCODE_CONFIG_CONTENT=")), &cfg)
		js := `const p=await import(process.argv[1]);const h=(await p.GolemPolicy())["tool.execute.before"];let denied=false;try{await h({tool:"bash"},{args:{command:"git push origin feature"}})}catch(e){denied=true}if(!denied)process.exit(3);await h({tool:"edit"},{args:{filePath:` + strconv.Quote(filepath.Join(repo, "main.go")) + `}});`
		cmd := exec.Command("node", "--input-type=module", "-e", js, cfg.Plugin[0])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("plugin: %v %s", err, out)
		}
	})
	diag, err := prepareTools(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	defer diag.close()
	argv, err := diag.claude([]string{"claude", "--print"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv, "|"), "--tools||--disallowedTools|mcp__*") {
		t.Fatalf("no-tools argv: %q", argv)
	}
	if diag.root == repo {
		t.Fatal("diagnoser can discover repository")
	}
	denier := exec.CommandContext(context.Background(), "sh", "-c", diag.command)
	denier.Stdin = bytes.NewBufferString(`{"tool_name":"Read","tool_input":{"file_path":"main.go"}}`)
	if err := denier.Run(); err == nil {
		t.Fatal("diagnostic hook allowed a tool")
	}
}
func TestTicketScopesReadHostPolicy(t *testing.T) {
	repo := t.TempDir()
	os.MkdirAll(filepath.Join(repo, ".golem"), 0755)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\ntool_policy:\n  allow_network: [host.test]\n  allow_worktree_only: true\n"), 0644)
	for _, root := range []string{filepath.Join(repo, ".golem", "tickets", "one"), filepath.Join(repo, ".golem", "tickets", "one", "worktree")} {
		os.MkdirAll(root, 0755)
		s, err := prepareTools(root, false)
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(s.dir, "policy.json"))
		s.close()
		if err != nil || !strings.Contains(string(b), "host.test") {
			t.Fatalf("host policy lost for %s: %s %v", root, b, err)
		}
	}
}
func TestOpenCodePolicyPreservesProviderConfig(t *testing.T) {
	s, err := prepareTools(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	env, err := s.openCodeEnv(`{"provider":{"local":{"name":"synthetic"}},"plugin":["file:///synthetic-existing.mjs"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env[0], "synthetic-existing") || !strings.Contains(env[0], `"provider"`) {
		t.Fatal("operator config dropped")
	}
	if _, err := s.openCodeEnv(`// unsupported`); err == nil {
		t.Fatal("unsupported config silently discarded")
	}
}

func TestFileURL(t *testing.T) {
	for path, want := range map[string]string{
		"C:/Users/x/policy.mjs": "file:///C:/Users/x/policy.mjs",
		"/tmp/golem/policy.mjs": "file:///tmp/golem/policy.mjs",
	} {
		if got := fileURL(path); got != want {
			t.Fatalf("fileURL(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestGolemBinarySelection(t *testing.T) {
	found := func(string) (string, error) { return "/path/golem", nil }
	missing := func(string) (string, error) { return "", os.ErrNotExist }
	self := func(p string) func() (string, error) { return func() (string, error) { return p, nil } }
	if got, err := golemBinary(self("/new/golem"), found); err != nil || got != "/new/golem" {
		t.Fatalf("running golem should win: %q %v", got, err)
	}
	if got, err := golemBinary(self("/bin/GOLEM.exe"), found); err != nil || got != "/bin/GOLEM.exe" {
		t.Fatalf("golem.exe should win: %q %v", got, err)
	}
	if got, err := golemBinary(self("/new/shem"), found); err != nil || got != "/path/golem" {
		t.Fatalf("shem should use PATH: %q %v", got, err)
	}
	if _, err := golemBinary(self("/new/shem"), missing); err == nil {
		t.Fatal("missing golem accepted")
	}
}

func TestClaudeSettingsKeepHooksEnabled(t *testing.T) {
	s, err := prepareTools(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	argv, err := s.claude([]string{"claude"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argv[len(argv)-1])
	if err != nil || !strings.Contains(string(data), `"disableAllHooks":false`) {
		t.Fatalf("settings must override project disableAllHooks: %s %v", data, err)
	}
}

func golemName() string {
	if runtime.GOOS == "windows" {
		return "golem.exe"
	}
	return "golem"
}

// prepareTools only needs a golem on PATH, so the tests get a stand-in.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "golem-stub-")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, golemName()), []byte("#!/bin/sh\n"), 0o755); err != nil {
		panic(err)
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
