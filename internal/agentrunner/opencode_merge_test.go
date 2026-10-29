package agentrunner

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A config opencode accepts but encoding/json does not (comments) must not
// be replaced: that would delete the operator's providers.
func TestOpenCodeConfigLeavesUnparseableFileAlone(t *testing.T) {
	dir := t.TempDir()
	seed := "{\n  // local provider\n  \"provider\": {\"x\": {}}\n}\n"
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenCodeConfig(dir); err != nil {
		t.Fatalf("writeOpenCodeConfig: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != seed {
		t.Errorf("unparseable config was rewritten:\n%s", got)
	}
}

// The operator's own permission rules survive; golem's denies still come
// last so they win under opencode's last-match rule.
func TestOpenCodeConfigMergesOperatorPermissions(t *testing.T) {
	dir := t.TempDir()
	seed := `{"model":"m","permission":{"bash":{"terraform *":"allow","git push *":"allow"},"edit":"ask","skill":"deny"}}`
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenCodeConfig(dir); err != nil {
		t.Fatalf("writeOpenCodeConfig: %v", err)
	}
	data, _ := os.ReadFile(path)
	var cfg struct {
		Model      string `json:"model"`
		Permission struct {
			Bash  map[string]string `json:"bash"`
			Edit  string            `json:"edit"`
			Skill string            `json:"skill"`
		} `json:"permission"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("merged config does not parse: %v\n%s", err, data)
	}
	p := cfg.Permission
	if cfg.Model != "m" || p.Edit != "ask" || p.Skill != "deny" || p.Bash["terraform *"] != "allow" {
		t.Errorf("operator settings lost:\n%s", data)
	}
	if p.Bash["git push *"] != "deny" || p.Bash["git *"] != "allow" {
		t.Errorf("golem rules missing or overridden:\n%s", data)
	}
	text := string(data)
	if strings.Index(text, `"terraform *"`) > strings.Index(text, `"sudo *"`) {
		t.Errorf("golem denies are not last:\n%s", data)
	}
	if strings.Index(text, `"model"`) > strings.Index(text, `"permission"`) {
		t.Errorf("top-level key order changed:\n%s", data)
	}

	// A second run changes nothing.
	if err := writeOpenCodeConfig(dir); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(path); string(again) != text {
		t.Errorf("second run changed the file:\n%s\n---\n%s", text, again)
	}
}

// A bare string for bash is opencode's catch-all; it replaces golem's.
func TestOpenCodeConfigBashString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte(`{"permission":{"bash":"allow"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenCodeConfig(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var cfg struct {
		Permission struct {
			Bash map[string]string `json:"bash"`
		} `json:"permission"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if cfg.Permission.Bash["*"] != "allow" || cfg.Permission.Bash["sudo *"] != "deny" {
		t.Errorf("bash string not merged as catch-all:\n%s", data)
	}
}

// Golem's rewrite of a tracked opencode.json must not reach `git add -A`.
func TestOpenCodeWorktreeSetupSkipsWorktreeForTrackedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "opencode.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "opencode.json"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := (OpenCode{}).WorktreeSetup(dir); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", dir, "ls-files", "-v", "opencode.json").Output()
	if err != nil || !strings.HasPrefix(string(out), "S ") {
		t.Errorf("ls-files -v = %q, %v; want skip-worktree flag", out, err)
	}
}

// `opencode run` auto-rejects an "ask" and ends the run, so golem's block
// must never produce one.
func TestOpenCodePermissionHasNoAsk(t *testing.T) {
	if s := string(opencodePermissionJSON()); strings.Contains(s, `"ask"`) {
		t.Errorf("permission block contains an ask: %s", s)
	}
}

// opencode writes $schema into a config that lacks it, so golem's file
// carries it already.
func TestOpenCodeConfigHasSchema(t *testing.T) {
	dir := t.TempDir()
	if err := writeOpenCodeConfig(dir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "opencode.json"))
	if !strings.Contains(string(data), `"$schema": "https://opencode.ai/config.json"`) {
		t.Errorf("no $schema:\n%s", data)
	}
}
