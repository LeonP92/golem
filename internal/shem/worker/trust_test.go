package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The container entrypoint trusts every directory under /repos that exists
// when it starts. Golem now clones a repository the first time it works a
// ticket for it, so that path does not exist yet at container start and the
// entrypoint never sees it — the agent then reports
//
//	Ignoring 34 permissions.allow entries from .claude/settings.json:
//	this workspace has not been trusted.
//
// and runs without the repository's own allow-list.
func TestSetWorkspaceTrusted(t *testing.T) {
	const repo = "/repos/omnicore-platform"

	read := func(t *testing.T, path string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatalf("parse back: %v (contents: %s)", err, b)
		}
		return cfg
	}
	trustedIn := func(cfg map[string]any, path string) bool {
		projects, _ := cfg["projects"].(map[string]any)
		entry, _ := projects[path].(map[string]any)
		trusted, _ := entry["hasTrustDialogAccepted"].(bool)
		return trusted
	}

	t.Run("trusts a workspace the config has never heard of", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(cfgPath, []byte(`{"projects":{}}`), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := setWorkspaceTrusted(cfgPath, repo); err != nil {
			t.Fatalf("setWorkspaceTrusted: %v", err)
		}
		if !trustedIn(read(t, cfgPath), repo) {
			t.Error("the workspace was not trusted")
		}
	})

	t.Run("preserves every key it does not understand", func(t *testing.T) {
		// This file holds auth state. Losing a field of it would be a far
		// worse outcome than an untrusted workspace, so the round trip has
		// to be lossless.
		cfgPath := filepath.Join(t.TempDir(), ".claude.json")
		seed := `{"oauthAccount":{"emailAddress":"a@b.c"},"numStartups":7,` +
			`"projects":{"/repos/test":{"hasTrustDialogAccepted":true,"history":["x"]}}}`
		if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := setWorkspaceTrusted(cfgPath, repo); err != nil {
			t.Fatalf("setWorkspaceTrusted: %v", err)
		}
		cfg := read(t, cfgPath)
		if acct, _ := cfg["oauthAccount"].(map[string]any); acct["emailAddress"] != "a@b.c" {
			t.Error("the account block was lost")
		}
		if n, _ := cfg["numStartups"].(float64); n != 7 {
			t.Errorf("numStartups = %v, want 7", cfg["numStartups"])
		}
		if !trustedIn(cfg, "/repos/test") {
			t.Error("an existing trusted project was disturbed")
		}
		projects, _ := cfg["projects"].(map[string]any)
		existing, _ := projects["/repos/test"].(map[string]any)
		if hist, _ := existing["history"].([]any); len(hist) != 1 {
			t.Error("an existing project's other fields were lost")
		}
		if !trustedIn(cfg, repo) {
			t.Error("the new workspace was not trusted")
		}
	})

	t.Run("leaves an absent config absent", func(t *testing.T) {
		// The entrypoint bootstraps this file only when it does not exist,
		// and that bootstrap is what sets up headless auth. Creating a stub
		// here would make it skip on the next start and break login.
		cfgPath := filepath.Join(t.TempDir(), ".claude.json")
		if err := setWorkspaceTrusted(cfgPath, repo); err != nil {
			t.Fatalf("setWorkspaceTrusted: %v", err)
		}
		if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
			t.Error("a config file was created where none existed")
		}
	})

	t.Run("reports malformed JSON rather than overwriting it", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), ".claude.json")
		if err := os.WriteFile(cfgPath, []byte(`{not json`), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := setWorkspaceTrusted(cfgPath, repo); err == nil {
			t.Fatal("malformed JSON was accepted")
		}
		b, _ := os.ReadFile(cfgPath)
		if string(b) != `{not json` {
			t.Error("the unreadable file was overwritten instead of reported")
		}
	})

	t.Run("is a no-op when already trusted", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), ".claude.json")
		seed := `{"projects":{"` + repo + `":{"hasTrustDialogAccepted":true}}}`
		if err := os.WriteFile(cfgPath, []byte(seed), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
		before, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if err := setWorkspaceTrusted(cfgPath, repo); err != nil {
			t.Fatalf("setWorkspaceTrusted: %v", err)
		}
		after, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if before.ModTime() != after.ModTime() || before.Size() != after.Size() {
			t.Error("an already-trusted config was rewritten")
		}
	})
}
