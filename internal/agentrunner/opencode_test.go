package agentrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeArgv(t *testing.T) {
	tests := []struct {
		name  string
		opts  Options
		model string
		want  string
	}{
		{"no model, no extra args", Options{}, "", "opencode run"},
		{"model only", Options{}, "anthropic/claude-sonnet-4-6", "opencode run --model anthropic/claude-sonnet-4-6"},
		{"extra args only", Options{ExtraArgs: []string{"--auto"}}, "", "opencode run --auto"},
		{"command override", Options{Command: "my-opencode"}, "", "my-opencode run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, err := OpenCode{opts: tt.opts}.argv(tt.model)
			if err != nil {
				t.Fatalf("argv: %v", err)
			}
			if got := strings.Join(argv, " "); got != tt.want {
				t.Errorf("argv = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenCodeRejectsInvalidModel(t *testing.T) {
	if _, err := (OpenCode{}).argv("bad model"); err == nil {
		t.Fatal("argv accepted a model id with a space")
	}
}

// Opencode's last matching permission rule wins, so the emission order is
// the security property: catch-all first, allows next, denies last.
func TestOpenCodePermissionOrder(t *testing.T) {
	raw := opencodePermissionJSON()
	var perm struct {
		Bash map[string]string `json:"bash"`
		Edit string            `json:"edit"`
	}
	if err := json.Unmarshal(raw, &perm); err != nil {
		t.Fatalf("permission JSON does not parse: %v\n%s", err, raw)
	}
	text := string(raw)
	catchAll := strings.Index(text, `"*"`)
	allowGit := strings.Index(text, `"git *":"allow"`)
	denyPush := strings.Index(text, `"git push --force *":"deny"`)
	if catchAll < 0 || allowGit < 0 || denyPush < 0 {
		t.Fatalf("missing rules: %s", text)
	}
	if !(catchAll < allowGit && allowGit < denyPush) {
		t.Errorf("rule order is not catch-all < allow < deny: %s", text)
	}
	if perm.Bash["*"] != "deny" {
		t.Errorf(`bash["*"] = %q, want "deny"`, perm.Bash["*"])
	}
	if perm.Bash["git push --force *"] != "deny" {
		t.Errorf("deny rule wrong: %q", perm.Bash["git push --force *"])
	}
	if perm.Edit != "allow" {
		t.Errorf("edit = %q, want allow", perm.Edit)
	}
}

// The config merge preserves unrelated top-level keys an operator set,
// mirroring the claude adapter's settings.json merge.
func TestOpenCodeConfigPreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	seed := `{"model": "anthropic/claude-opus-4-5", "permission": {"old": true}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenCodeConfig(dir); err != nil {
		t.Fatalf("writeOpenCodeConfig: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.json"))
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("merged config does not parse: %v\n%s", err, data)
	}
	var model string
	if err := json.Unmarshal(parsed["model"], &model); err != nil || model != "anthropic/claude-opus-4-5" {
		t.Errorf("operator's model key not preserved: %s", parsed["model"])
	}
	if !strings.Contains(string(parsed["permission"]), `"old"`) {
		t.Errorf("operator permission key dropped by the merge: %s", parsed["permission"])
	}
}

func TestOpenCodeGenerateArtifacts(t *testing.T) {
	dir := t.TempDir()
	roles := map[string]string{"developer": "You write code."}
	if err := (OpenCode{}).GenerateArtifacts(dir, roles); err != nil {
		t.Fatalf("GenerateArtifacts: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".opencode", "agents", "golem-developer.md"))
	if err != nil {
		t.Fatalf("read agent file: %v", err)
	}
	for _, want := range []string{"mode: subagent", "You write code."} {
		if !strings.Contains(string(data), want) {
			t.Errorf("agent file missing %q:\n%s", want, data)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); err != nil {
		t.Errorf("opencode.json not written: %v", err)
	}
}
