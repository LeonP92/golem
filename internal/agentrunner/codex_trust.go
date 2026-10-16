package agentrunner

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/leonp92/golem/internal/agentenv"
)

// codexConfigMu serialises read-modify-write of ~/.codex/config.toml. The
// file is shared by every repository, so unlike the per-repo work around it
// this cannot be guarded by repoMutex.
var codexConfigMu sync.Mutex

// PrepareHost marks repoPath trusted in ~/.codex/config.toml.
//
// Codex loads a project's .codex/ layer — including the rules file
// GenerateArtifacts writes — only when the project is trusted, and in
// `codex exec` an unanswered trust prompt is not answerable, which leaves
// the run untrusted and the rules ignored. Trust is recorded per absolute
// path as
//
//	[projects."/abs/path"]
//	trust_level = "trusted"
//
// (https://developers.openai.com/codex/config-advanced). The container
// entrypoint does this for directories that exist when the container
// starts; this hook covers repositories golem clones on demand afterwards,
// the same gap the claude adapter's PrepareHost covers for .claude.json.
func (c Codex) PrepareHost(repoPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot locate the home directory to trust %s: %w", repoPath, err)
	}
	homes := []string{home}
	acct := agentenv.User()
	if acct != nil {
		homes = append(homes, acct.Home) // the agent reads its own config
	}
	var errs []error
	for _, h := range homes {
		configPath := filepath.Join(h, ".codex", "config.toml")
		if err := trustCodexProject(configPath, repoPath); err != nil {
			errs = append(errs, err)
			continue
		}
		// This process is root and has just written a file the AGENT has to
		// read. Hand it back, or the agent's codex cannot read its own
		// config — the same ownership problem as ~/.claude.json, documented
		// on the claude adapter's PrepareHost.
		if acct != nil && h == acct.Home {
			if err := os.Chown(configPath, int(acct.UID), int(acct.GID)); err != nil {
				errs = append(errs, fmt.Errorf("could not hand %s back to %s: %w", configPath, acct.Name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// trustCodexProject appends a trusted-projects table for repoPath to the
// codex config at configPath, creating the file when absent. An existing
// entry for the path — in any spelling codex itself writes — is left alone.
//
// The append form is safe TOML: [projects."<path>"] introduces a new table,
// and no other key in the file is disturbed. Codex itself appends trust
// entries to this same file when a user answers its trust prompt.
func trustCodexProject(configPath, repoPath string) error {
	codexConfigMu.Lock()
	defer codexConfigMu.Unlock()

	raw, err := os.ReadFile(configPath) //nolint:gosec // path derived from the home directory
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}
	if strings.Contains(string(raw), fmt.Sprintf("%q", repoPath)) {
		return nil // an entry naming this path already exists; trust it or not, it is the operator's
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(configPath), err)
	}
	entry := fmt.Sprintf("\n[projects.%q]\ntrust_level = \"trusted\"\n", repoPath)
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // read by the agent account
	if err != nil {
		return fmt.Errorf("opening %s: %w", configPath, err)
	}
	defer f.Close()
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}
	log.Printf("executor: trusted codex project %s", repoPath)
	return nil
}
