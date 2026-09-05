package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/blog"
)

func TestObserverDispatchAppendsFindingFromRealCommit(t *testing.T) {
	repo, worktree, ticketID := setUpTicketForStepTest(t)
	os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte("backend: claude-code\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, ".golem", "roles"), 0o755)
	os.WriteFile(filepath.Join(repo, ".golem", "roles", "convention-enforcer.md"), []byte("# role\n"), 0o644)

	sha := gitCommit(t, worktree, "feature.go", "package main\n", "add feature")

	fakeClaudeOnPath(t, "FINDING: missing doc comment")

	var stdout, stderr bytes.Buffer
	code := ObserverDispatch([]string{"--repo", repo, "--ticket", ticketID, "--role", "convention-enforcer", "--commit", sha}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("ObserverDispatch failed: exit %d, stderr=%s", code, stderr.String())
	}

	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", ticketID, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Type == blog.TypeFinding && strings.Contains(e.Message, "missing doc comment") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a FINDING entry from the dispatched review, got %+v", entries)
	}
}

// fakeClaudeOnPath builds a small Go binary that prints output and places it
// on PATH as "claude" (or "claude.exe" on Windows). The output argument is
// the text the fake should print to stdout.
func fakeClaudeOnPath(t *testing.T, output string) {
	t.Helper()
	dir := t.TempDir()

	// Write a tiny Go main program that just prints the desired output.
	src := `package main

import "fmt"

func main() {
	fmt.Println(` + "`" + output + "`" + `)
}
`
	srcPath := filepath.Join(dir, "fakeclaude.go")
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("WriteFile fakeclaude.go: %v", err)
	}

	binName := "claude"
	if runtime.GOOS == "windows" {
		binName = "claude.exe"
	}
	binPath := filepath.Join(dir, binName)
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building fake claude: %v\n%s", err, out)
	}

	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatalf("fake claude not on PATH: %v", err)
	}
}
