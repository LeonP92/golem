package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The agent-account block cannot be driven from this harness: it resolves the
// home directory out of /etc/passwd and chowns to a user that only exists in
// the image. These assertions pin the two properties that broke it; the
// behaviour itself is covered by running the container.
func TestEntrypointCopiesTheSessionToTheAgentHome(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "deploy", "shem-entrypoint.sh"))
	if err != nil {
		t.Fatalf("read entrypoint: %v", err)
	}
	script := string(src)

	// Gated on the mounted directory, not on .claude.json. On a Windows host
	// ~/.claude.json sits beside ~/.claude rather than inside it, so it never
	// reaches the container — and the agent got a bootstrap config with no
	// .credentials.json next to it, which claude reports as "Not logged in".
	if strings.Contains(script, `[ -n "$CLAUDE_HOME" ] && [ -f "$CLAUDE_JSON" ]`) {
		t.Error("the agent's session copy is gated on $CLAUDE_JSON; a mount that carries " +
			"only ~/.claude then leaves the agent with no credentials")
	}
	if !strings.Contains(script, `[ -n "$CLAUDE_HOME" ] && [ -d "$CLAUDE_DIR" ]`) {
		t.Error("the agent's session copy is not gated on the mounted directory")
	}

	// A copy that fails has to say so. Silenced, it leaves a config that looks
	// right and has no credentials in it.
	for _, silenced := range []string{
		`cp -R "$CLAUDE_DIR/." "$agent_home/.claude/" 2>/dev/null || true`,
		`cp -f "$CLAUDE_JSON" "$agent_home/.claude.json" 2>/dev/null || true`,
	} {
		if strings.Contains(script, silenced) {
			t.Errorf("a failing copy into the agent home is silenced: %s", silenced)
		}
	}

	// And the end state is checked, so a missing credential is named at start
	// rather than as a failing phase later.
	if !strings.Contains(script, `.credentials.json" ]`) {
		t.Error("the entrypoint never checks that the agent ended up with credentials")
	}
}
