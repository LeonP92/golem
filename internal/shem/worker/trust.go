package worker

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/leonp92/golem/internal/agentenv"
)

// claudeJSONMu serialises read-modify-write of ~/.claude.json. The file is
// shared by every repository, so unlike the per-repo work around it this
// cannot be guarded by repoMutex.
var claudeJSONMu sync.Mutex

// trustWorkspace pre-accepts Claude Code's workspace trust dialog for
// repoPath, so the agent honours the repository's own .claude/settings.json
// instead of reporting
//
//	Ignoring 34 permissions.allow entries from .claude/settings.json:
//	this workspace has not been trusted.
//
// The container entrypoint already does this, but only for directories that
// exist when the container starts. Golem now clones a repository the first
// time it works a ticket for it, so the path does not exist yet at that
// point and the entrypoint cannot see it — every on-demand clone ran with
// its permissions ignored.
//
// Failure is logged and not returned: an untrusted workspace degrades (the
// agent warns and falls back to asking) rather than breaking, so it is not
// worth failing a ticket that would otherwise run.
func trustWorkspace(repoPath string) {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("executor: cannot locate the home directory to trust %s: %v", repoPath, err)
		return
	}
	homes := []string{home}
	if acct := agentenv.User(); acct != nil {
		homes = append(homes, acct.Home) // the agent reads its own config
	}
	for _, h := range homes {
		if err := setWorkspaceTrusted(filepath.Join(h, ".claude.json"), repoPath); err != nil {
			log.Printf("executor: could not trust workspace %s: %v", repoPath, err)
		}
	}
}

// setWorkspaceTrusted marks repoPath trusted in the Claude Code config at
// configPath.
//
// An absent config is left absent rather than created. The entrypoint
// bootstraps that file only when it does not exist, and creating a stub here
// would make that bootstrap skip on the next start — taking the headless auth
// setup with it. Trust is worth having; it is not worth breaking login for.
func setWorkspaceTrusted(configPath, repoPath string) error {
	claudeJSONMu.Lock()
	defer claudeJSONMu.Unlock()

	raw, err := os.ReadFile(configPath) //nolint:gosec // path derived from the home directory
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}

	// Decoded into a map so every key this code does not know about survives
	// the round trip — the file holds auth state, and losing a field of it
	// would be a far worse outcome than an untrusted workspace.
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parsing %s: %w", configPath, err)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	projects, _ := cfg["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
		cfg["projects"] = projects
	}
	entry, _ := projects[repoPath].(map[string]any)
	if entry == nil {
		entry = map[string]any{}
		projects[repoPath] = entry
	}
	if trusted, ok := entry["hasTrustDialogAccepted"].(bool); ok && trusted {
		return nil // already trusted; leave the file alone
	}
	entry["hasTrustDialogAccepted"] = true

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", configPath, err)
	}
	// Written via a temp file and renamed: Claude Code reads this file, and a
	// partial write would leave it holding invalid JSON.
	tmp := configPath + ".golem.tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replacing %s: %w", configPath, err)
	}
	log.Printf("executor: trusted workspace %s", repoPath)
	return nil
}
