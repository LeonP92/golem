package agentrunner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
)

// fakeClaude puts a `claude` on PATH that writes the given text to each
// stream and exits non-zero, so the test exercises the real RunAgent.
func fakeClaude(t *testing.T, stdout, stderr string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"cat > /dev/null\n" + // consume the prompt on stdin
		"printf '%s' " + shellQuote(stdout) + "\n" +
		"printf '%s' " + shellQuote(stderr) + " 1>&2\n" +
		"exit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// A failed agent run used to report only stderr. Claude Code puts most of
// what goes wrong on stdout — "Not logged in · Please run /login", an
// unreadable setting, a refused tool — so the reason was discarded while an
// unrelated line on the other stream was presented as the explanation. That
// is exactly what happened in practice: stderr held one line about a
// SessionEnd hook, and the actual cause was never printed.
func TestFailedAgentRunReportsBothStreams(t *testing.T) {
	t.Run("stdout reaches the error", func(t *testing.T) {
		dir := fakeClaude(t, "Not logged in · Please run /login", "SessionEnd hook failed: not found")
		_, err := agentrunner.ClaudeCode{RepoRoot: dir}.RunAgent("graph-builder", agentrunner.Context{})
		if err == nil {
			t.Fatal("a non-zero exit was reported as success")
		}
		msg := err.Error()
		if !strings.Contains(msg, "Not logged in") {
			t.Errorf("the reason on stdout was discarded: %v", msg)
		}
		if !strings.Contains(msg, "SessionEnd hook failed") {
			t.Errorf("stderr was dropped: %v", msg)
		}
		if !strings.Contains(msg, "graph-builder") {
			t.Errorf("the role is not named: %v", msg)
		}
	})

	t.Run("an empty stream contributes no heading", func(t *testing.T) {
		dir := fakeClaude(t, "only on stdout", "")
		_, err := agentrunner.ClaudeCode{RepoRoot: dir}.RunAgent("graph-builder", agentrunner.Context{})
		if err == nil {
			t.Fatal("expected an error")
		}
		if strings.Contains(err.Error(), "--- stderr ---") {
			t.Errorf("an empty stderr still produced a heading: %v", err)
		}
		if !strings.Contains(err.Error(), "--- stdout ---") {
			t.Errorf("stdout heading missing: %v", err)
		}
	})

	t.Run("a long stream is truncated to its tail", func(t *testing.T) {
		// The tail is what carries the failure; the head of a long answer is
		// not worth putting in an error.
		long := strings.Repeat("a", 3000) + "THE-ACTUAL-FAILURE"
		dir := fakeClaude(t, long, "")
		_, err := agentrunner.ClaudeCode{RepoRoot: dir}.RunAgent("graph-builder", agentrunner.Context{})
		if err == nil {
			t.Fatal("expected an error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "THE-ACTUAL-FAILURE") {
			t.Error("truncation dropped the tail, which is the part that matters")
		}
		if !strings.Contains(msg, "truncated") {
			t.Error("truncation is not disclosed")
		}
		if len(msg) > 4000 {
			t.Errorf("error is %d bytes; truncation did not apply", len(msg))
		}
	})
}
