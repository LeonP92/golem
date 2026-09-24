package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The shem is root and writes into the AGENT's home. Whatever it leaves
// behind has to stay readable by the agent.
//
// Review finding 1: 42ce266 started writing the agent's ~/.claude.json to
// add workspace trust, with no chown. The shem runs as root, so on any
// on-demand clone the file became root:golem-agent 0600 and the agent could
// no longer read its own config — `claude --print` reports "Not logged in"
// and the phase hangs with no output. shem-entrypoint.sh chowns for exactly
// this reason, but only once at container start.
//
// This test cannot run as root in CI, so it pins the mode rather than the
// ownership: 0600 is the bug's fingerprint and 0644 is what makes the file
// survive being written by a different user than the one that reads it. The
// chown itself is asserted by the call being made at all — see
// setWorkspaceTrustedFor's signature taking the account.
func TestSetWorkspaceTrusted_LeavesTheFileAgentReadable(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(configPath, []byte(`{"hasCompletedSetup":true,"projects":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := setWorkspaceTrusted(configPath, "/repos/newly-cloned"); err != nil {
		t.Fatalf("setWorkspaceTrusted: %v", err)
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o044 == 0 {
		t.Errorf("mode = %#o; a file written by root at 0600 is unreadable by the agent "+
			"account that has to load it, and the phase then hangs on \"Not logged in\"", perm)
	}

	// The write must still have done its job.
	var cfg map[string]any
	data, _ := os.ReadFile(configPath) //nolint:errcheck
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config is not valid JSON after the write: %v", err)
	}
	projects, _ := cfg["projects"].(map[string]any)
	entry, _ := projects["/repos/newly-cloned"].(map[string]any)
	if entry == nil || entry["hasTrustDialogAccepted"] != true {
		t.Errorf("the repo was not trusted: %v", cfg)
	}
}
