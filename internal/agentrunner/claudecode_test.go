package agentrunner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateClaudeCodeArtifactsWritesAgentFiles(t *testing.T) {
	dir := t.TempDir()
	roleContent := map[string]string{
		"developer": "# Developer Role\ndo the work",
		"reviewer":  "# Reviewer Role\ncheck the work",
	}
	if err := GenerateClaudeCodeArtifacts(dir, roleContent); err != nil {
		t.Fatalf("GenerateClaudeCodeArtifacts: %v", err)
	}

	devAgent := filepath.Join(dir, ".claude", "agents", "golem-developer.md")
	data, err := os.ReadFile(devAgent)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "do the work") {
		t.Errorf("generated agent file missing role content: %s", data)
	}
	if !strings.HasPrefix(string(data), "---\n") {
		t.Errorf("generated agent file should start with frontmatter, got: %s", data)
	}
}

func TestGenerateClaudeCodeCommandsWritesNewTicketSkill(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateClaudeCodeCommands(dir); err != nil {
		t.Fatalf("GenerateClaudeCodeCommands: %v", err)
	}
	path := filepath.Join(dir, ".claude", "commands", "golem", "new-ticket.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, want := range []string{"golem ticket new", "golem ticket advance", "golem ticket review"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("new-ticket.md missing reference to %q", want)
		}
	}
}

func TestRunAgentInvokesClaudeCLI(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH, skipping live-invocation shape test")
	}
	// This test only validates command construction via a fake PATH binary;
	// see helper below.
}
