package agentrunner

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// settingsWriters are the two code paths that write .claude/settings.json.
// Both must merge into what is already there, never overwrite it.
var settingsWriters = []struct {
	name  string
	write func(t *testing.T, root string) error
}{
	{"WorktreeSetup", func(t *testing.T, root string) error {
		return ClaudeCode{opts: Options{RepoRoot: root}}.WorktreeSetup(root)
	}},
	{"GenerateClaudeCodeArtifacts", func(t *testing.T, root string) error {
		return GenerateClaudeCodeArtifacts(root, map[string]string{"developer": "x"})
	}},
}

func writeSettings(t *testing.T, root, content string) string {
	t.Helper()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readSettings(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}
	return data
}

type parsedSettings struct {
	raw   map[string]json.RawMessage
	perms map[string]json.RawMessage
	allow []string
	deny  []string
}

func parseSettings(t *testing.T, data []byte) parsedSettings {
	t.Helper()
	var p parsedSettings
	if err := json.Unmarshal(data, &p.raw); err != nil {
		t.Fatalf("settings.json is not a JSON object: %v\n%s", err, data)
	}
	if err := json.Unmarshal(p.raw["permissions"], &p.perms); err != nil {
		t.Fatalf("permissions is not an object: %v", err)
	}
	if err := json.Unmarshal(p.perms["allow"], &p.allow); err != nil {
		t.Fatalf("permissions.allow: %v", err)
	}
	if err := json.Unmarshal(p.perms["deny"], &p.deny); err != nil {
		t.Fatalf("permissions.deny: %v", err)
	}
	return p
}

func assertGolemRules(t *testing.T, p parsedSettings) {
	t.Helper()
	for _, want := range claudeCodeAllowList {
		if !slices.Contains(p.allow, want) {
			t.Errorf("allow list missing golem rule %q", want)
		}
	}
	for _, want := range claudeCodeDenyList {
		if !slices.Contains(p.deny, want) {
			t.Errorf("deny list missing golem rule %q", want)
		}
	}
	for name, list := range map[string][]string{"allow": p.allow, "deny": p.deny} {
		seen := map[string]bool{}
		for _, r := range list {
			if seen[r] {
				t.Errorf("%s list has duplicate %q", name, r)
			}
			seen[r] = true
		}
	}
}

// The committed file grants the project's gate
// scripts and carries other fields; a ticket worktree must keep all of it.
func TestSettingsWritersPreserveCommittedSettings(t *testing.T) {
	committed := `{
  "permissions": {
    "allow": ["Bash(git *)", "Bash(sh scripts/*)", "Bash(./scripts/*)", "Bash(gofmt *)", "Bash(govulncheck *)", "Bash(a && b)"],
    "deny": ["Bash(curl *)"],
    "ask": ["Bash(docker *)"],
    "defaultMode": "acceptEdits"
  },
  "env": {"GOFLAGS": "-mod=mod"},
  "hooks": {"Stop": []}
}`
	extraAllow := []string{"Bash(sh scripts/*)", "Bash(./scripts/*)", "Bash(gofmt *)", "Bash(govulncheck *)", "Bash(a && b)"}

	for _, w := range settingsWriters {
		t.Run(w.name, func(t *testing.T) {
			root := t.TempDir()
			writeSettings(t, root, committed)
			if err := w.write(t, root); err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			data := readSettings(t, root)
			p := parseSettings(t, data)

			assertGolemRules(t, p)
			for _, want := range extraAllow {
				if !slices.Contains(p.allow, want) {
					t.Errorf("committed allow entry %q was dropped", want)
				}
			}
			if !slices.Contains(p.deny, "Bash(curl *)") {
				t.Error("committed deny entry was dropped")
			}
			// Existing order first, golem's additions after.
			if p.allow[1] != "Bash(sh scripts/*)" {
				t.Errorf("existing allow order not kept: %v", p.allow[:3])
			}
			for _, key := range []string{"env", "hooks"} {
				if _, ok := p.raw[key]; !ok {
					t.Errorf("top-level field %q was dropped", key)
				}
			}
			for _, key := range []string{"ask", "defaultMode"} {
				if _, ok := p.perms[key]; !ok {
					t.Errorf("permissions field %q was dropped", key)
				}
			}
			if data[len(data)-1] != '\n' {
				t.Error("settings.json has no trailing newline")
			}
			if escapedAmp := []byte{92 /* backslash */, 'u', '0', '0', '2', '6'}; bytes.Contains(data, escapedAmp) {
				t.Errorf("a rule containing && was HTML-escaped:\n%s", data)
			}
		})
	}
}

// With nothing committed the output is exactly what WorktreeSetup always
// wrote (golem's two lists), now with a trailing newline.
func TestSettingsWritersWithoutExistingFile(t *testing.T) {
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	doc.Permissions.Allow = claudeCodeAllowList
	doc.Permissions.Deny = claudeCodeDenyList
	want, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')

	for _, w := range settingsWriters {
		t.Run(w.name, func(t *testing.T) {
			root := t.TempDir()
			if err := w.write(t, root); err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			if got := readSettings(t, root); string(got) != string(want) {
				t.Errorf("settings.json =\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// A file the agent could not parse either is replaced with golem's defaults
// rather than failing worktree creation; `golem init` has always done this.
func TestSettingsWritersReplaceUnusableFile(t *testing.T) {
	cases := []struct{ name, content string }{
		{"malformed", `{"permissions": {"allow": [`},
		{"not an object", `["Bash(*)"]`},
		{"null", `null`},
		{"permissions not an object", `{"permissions": "all", "env": {"A": "1"}}`},
		{"allow not a string list", `{"permissions": {"allow": "Bash(*)", "deny": ["Bash(curl *)"]}}`},
	}
	for _, w := range settingsWriters {
		for _, tc := range cases {
			t.Run(w.name+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				writeSettings(t, root, tc.content)
				if err := w.write(t, root); err != nil {
					t.Fatalf("%s: %v", w.name, err)
				}
				assertGolemRules(t, parseSettings(t, readSettings(t, root)))
			})
		}
	}
	// What is still usable around the broken part survives.
	root := t.TempDir()
	writeSettings(t, root, `{"permissions": {"allow": "Bash(*)", "deny": ["Bash(curl *)"]}, "env": {"A": "1"}}`)
	if err := (ClaudeCode{}).WorktreeSetup(root); err != nil {
		t.Fatal(err)
	}
	p := parseSettings(t, readSettings(t, root))
	if !slices.Contains(p.deny, "Bash(curl *)") {
		t.Error("valid deny list was dropped because allow was malformed")
	}
	if _, ok := p.raw["env"]; !ok {
		t.Error("top-level env was dropped")
	}
}

// A second run changes nothing, and a committed file that already carries
// golem's rules is not rewritten at all, so the worktree stays clean.
func TestSettingsWritersAreIdempotent(t *testing.T) {
	for _, w := range settingsWriters {
		t.Run(w.name, func(t *testing.T) {
			root := t.TempDir()
			writeSettings(t, root, `{"permissions":{"allow":["Bash(sh scripts/*)"]},"model":"x"}`)
			if err := w.write(t, root); err != nil {
				t.Fatal(err)
			}
			first := readSettings(t, root)
			if err := w.write(t, root); err != nil {
				t.Fatal(err)
			}
			if second := readSettings(t, root); string(second) != string(first) {
				t.Errorf("second run changed the file:\n%s\nthen\n%s", first, second)
			}
		})
	}

	// Already complete, in a layout golem would not produce itself: untouched.
	allow, _ := json.Marshal(append([]string{"Bash(gofmt *)"}, claudeCodeAllowList...))
	deny, _ := json.Marshal(claudeCodeDenyList)
	committed := `{"permissions": {"allow": ` + string(allow) + `, "deny": ` + string(deny) + `}}` // no newline
	root := t.TempDir()
	writeSettings(t, root, committed)
	if err := (ClaudeCode{}).WorktreeSetup(root); err != nil {
		t.Fatal(err)
	}
	if got := readSettings(t, root); string(got) != committed {
		t.Errorf("complete committed file was rewritten:\n%s", got)
	}
}

// The Edit rule must be anchored to the settings file's directory. Claude Code
// resolves an unanchored pattern against the session's current directory, so
// with "Edit(**)" an agent that cd's into its worktree can no longer write the
// ticket's spec.md or plan.md one level up, and --print cannot answer the
// permission prompt that follows.
func TestEditRuleIsAnchoredToSettingsRoot(t *testing.T) {
	if !slices.Contains(claudeCodeAllowList, "Edit(/**)") {
		t.Errorf("allow list lacks the anchored Edit(/**) rule: %v", claudeCodeAllowList)
	}
	if slices.Contains(claudeCodeAllowList, "Edit(**)") {
		t.Error("allow list has the cwd-relative Edit(**) rule, which a cd into the worktree narrows")
	}
}
