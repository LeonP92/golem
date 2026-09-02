package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildGolemBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "golem")
	if runtime.GOOS == "windows" {
		binPath += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/leonpham/golem/cmd/golem")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return binPath
}

func runGolem(t *testing.T, bin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	code = 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("running %s %v: %v", bin, args, err)
	}
	return out.String(), errBuf.String(), code
}

func TestCLILifecycleThroughRealBinary(t *testing.T) {
	bin := buildGolemBinary(t)
	repo := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture"), 0o644)
	run("add", "README.md")
	run("commit", "-q", "-m", "initial")

	if _, stderr, code := runGolem(t, bin, "init", "--repo", repo, "--backend", "claude-code"); code != 0 {
		t.Fatalf("golem init failed: %s", stderr)
	}

	fakeClaudeDir := t.TempDir()
	fakeSrc := `package main
import "fmt"
func main() { fmt.Println("FINDING: missing doc comment") }
`
	fakeSrcPath := filepath.Join(fakeClaudeDir, "fakeclaude.go")
	os.WriteFile(fakeSrcPath, []byte(fakeSrc), 0o644)
	fakeClaudeName := "claude"
	if runtime.GOOS == "windows" {
		fakeClaudeName = "claude.exe"
	}
	fakeClaudePath := filepath.Join(fakeClaudeDir, fakeClaudeName)
	buildCmd := exec.Command("go", "build", "-o", fakeClaudePath, fakeSrcPath)
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("building fake claude: %v\n%s", err, out)
	}
	os.Setenv("PATH", fakeClaudeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, stderr, code := runGolem(t, bin, "ticket", "new", "--repo", repo, "--id", "cli1", "add feature"); code != 0 {
		t.Fatalf("golem ticket new failed: %s", stderr)
	}

	// Read worktree path from state.json
	stateData, err := os.ReadFile(filepath.Join(repo, ".golem", "tickets", "cli1", "state.json"))
	if err != nil {
		t.Fatalf("reading state.json: %v", err)
	}
	var state struct {
		WorktreePath string `json:"worktree_path"`
	}
	if err := json.Unmarshal(stateData, &state); err != nil {
		t.Fatalf("parsing state.json: %v", err)
	}
	worktreePath := state.WorktreePath

	os.WriteFile(filepath.Join(worktreePath, "feature.go"), []byte("package main\n"), 0o644)
	commitCmd := exec.Command("git", "add", "feature.go")
	commitCmd.Dir = worktreePath
	commitCmd.Run()
	commitCmd = exec.Command("git", "commit", "-q", "-m", "add feature")
	commitCmd.Dir = worktreePath
	commitCmd.Run()
	shaCmd := exec.Command("git", "rev-parse", "HEAD")
	shaCmd.Dir = worktreePath
	shaOut, _ := shaCmd.Output()
	sha := strings.TrimSpace(string(shaOut))

	if _, stderr, code := runGolem(t, bin, "observer", "dispatch", "--repo", repo, "--ticket", "cli1", "--role", "convention-enforcer", "--commit", sha); code != 0 {
		t.Fatalf("golem observer dispatch failed: %s", stderr)
	}

	stdout, stderr, code := runGolem(t, bin, "tickets", "--repo", repo)
	if code != 0 {
		t.Fatalf("golem tickets failed: %s", stderr)
	}
	if !strings.Contains(stdout, "cli1") {
		t.Errorf("expected cli1 in tickets output, got: %s", stdout)
	}

	logPath := filepath.Join(repo, ".golem", "tickets", "cli1", "log.jsonl")
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile log: %v", err)
	}
	if !strings.Contains(string(logData), "missing doc comment") {
		t.Fatalf("expected the observer-dispatched finding in the log, got:\n%s", logData)
	}
}
