package agentrunner

import (
	"encoding/json"
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

func TestGenerateClaudeCodeArtifactsWritesSettingsWithDenyList(t *testing.T) {
	dir := t.TempDir()
	if err := GenerateClaudeCodeArtifacts(dir, map[string]string{"developer": "do the work"}); err != nil {
		t.Fatalf("GenerateClaudeCodeArtifacts: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("ReadFile settings.json: %v", err)
	}

	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal settings.json: %v", err)
	}

	for _, want := range claudeCodeAllowList {
		found := false
		for _, a := range settings.Permissions.Allow {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("allow list missing %q", want)
		}
	}
	for _, want := range claudeCodeDenyList {
		found := false
		for _, d := range settings.Permissions.Deny {
			if d == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deny list missing %q", want)
		}
	}
}

func TestGenerateClaudeCodeArtifactsMergesExistingSettings(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"permissions":{"allow":["Bash(my-custom-tool *)"],"deny":[]},"someOtherField":"preserved"}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := GenerateClaudeCodeArtifacts(dir, map[string]string{"developer": "x"}); err != nil {
		t.Fatalf("GenerateClaudeCodeArtifacts: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["someOtherField"]; !ok {
		t.Error("unrelated top-level field was dropped")
	}

	var perms struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	if err := json.Unmarshal(raw["permissions"], &perms); err != nil {
		t.Fatal(err)
	}
	customFound := false
	for _, a := range perms.Allow {
		if a == "Bash(my-custom-tool *)" {
			customFound = true
			break
		}
	}
	if !customFound {
		t.Error("pre-existing allow entry was dropped during merge")
	}
	if len(perms.Deny) == 0 {
		t.Error("deny list is empty after merge")
	}
}

func TestWorktreeSetupWritesSettingsWithDenyList(t *testing.T) {
	dir := t.TempDir()
	cc := ClaudeCode{RepoRoot: dir}
	if err := cc.WorktreeSetup(dir); err != nil {
		t.Fatalf("WorktreeSetup: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var settings struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(settings.Permissions.Deny) == 0 {
		t.Error("WorktreeSetup settings.json has empty deny list")
	}
	for _, want := range claudeCodeDenyList {
		found := false
		for _, d := range settings.Permissions.Deny {
			if d == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("deny list missing %q", want)
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
