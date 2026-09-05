package agentrunner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const agentFrontmatterTemplate = `---
name: golem-%s
description: Golem %s role
---

`

// GenerateClaudeCodeArtifacts translates neutral role content into
// Claude Code subagent definitions. This is a projection of the neutral
// role files, not the source of truth (spec: Architecture Overview) —
// re-running it after a role file changes regenerates the artifact.
func GenerateClaudeCodeArtifacts(repoRoot string, roleContent map[string]string) error {
	agentsDir := filepath.Join(repoRoot, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}
	for role, content := range roleContent {
		frontmatter := fmt.Sprintf(agentFrontmatterTemplate, role, role)
		path := filepath.Join(agentsDir, "golem-"+role+".md")
		if err := os.WriteFile(path, []byte(frontmatter+content), 0o644); err != nil {
			return err
		}
	}

	settingsPath := filepath.Join(repoRoot, ".claude", "settings.json")
	existing, err := os.ReadFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Parse into a raw map so unknown top-level fields are preserved.
	raw := map[string]json.RawMessage{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &raw)
	}

	// Extract the current allow list, if any.
	var allow []string
	if permRaw, ok := raw["permissions"]; ok {
		var perms struct {
			Allow []string `json:"allow"`
		}
		if err := json.Unmarshal(permRaw, &perms); err == nil {
			allow = perms.Allow
		}
	}

	// Union with our entries (dedup).
	seen := make(map[string]bool, len(allow))
	for _, e := range allow {
		seen[e] = true
	}
	for _, e := range claudeCodeAllowList {
		if !seen[e] {
			allow = append(allow, e)
		}
	}

	// Write back: merge our permissions into the raw map to preserve other fields.
	permsDoc := struct {
		Allow []string `json:"allow"`
	}{Allow: allow}
	permBytes, err := json.Marshal(permsDoc)
	if err != nil {
		return err
	}
	raw["permissions"] = permBytes
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, append(out, '\n'), 0o644)
}

// ClaudeCode dispatches role invocations as one-shot `claude -p` calls.
// It stores no credentials — it relies entirely on the host's own
// `claude` CLI authentication (spec: Backend Adapters).
type ClaudeCode struct {
	RepoRoot string
	Model    string
}

const newTicketSkill = `---
name: golem-new-ticket
description: Start a new Golem ticket - brainstorm, plan, implement, review
---

Given a one-line description of what to build:

1. Run ` + "`golem ticket new --id <slug> \"<description>\"`" + ` to create
   the ticket, worktree, and branch.
2. Brainstorm a spec with the human, following ` + "`.golem/roles/spec-adherence.md`" + `.
   Get explicit approval before continuing.
3. Run ` + "`golem ticket advance --ticket <id> --to plan`" + `.
4. As the developer (` + "`.golem/roles/developer.md`" + `), produce ordered
   plan steps, each with an expected diff-line-count. Get human approval.
5. Run ` + "`golem ticket advance --ticket <id> --to implement`" + `.
6. Execute each plan step: before starting, run
   ` + "`golem ticket set-step --ticket <id> --expected-lines <n>`" + `; commit
   at each small logical unit; after every commit, run
   ` + "`golem ticket check-bloat`" + ` and the two ` + "`golem observer dispatch`" + `
   calls from ` + "`.golem/roles/developer.md`" + `; check the log for
   unresolved BLOCKERs before continuing.
7. When all steps are done, run
   ` + "`golem ticket review --ticket <id>`" + ` and report the result to the
   human.
8. Once the human approves the reviewed work, run
   ` + "`golem ticket close --ticket <id>`" + ` — this promotes any
   generalized soul entries the reviewer proposed and tears down the
   worktree.
`

const ticketsSkill = `---
name: golem-tickets
description: List all active Golem tickets and their phase
---

Run ` + "`golem tickets --repo .`" + ` and report the output.
`

var claudeCodeAllowList = []string{
	// Version control
	"Bash(git *)",
	// Golem CLI
	"Bash(golem *)",
	"Bash(./golem *)",
	"Bash(./golem.exe *)",
	// Language build tools
	"Bash(go *)",
	"Bash(python3 *)",
	"Bash(python *)",
	"Bash(pip *)",
	"Bash(pip3 *)",
	"Bash(npm *)",
	"Bash(npx *)",
	"Bash(node *)",
	"Bash(cargo *)",
	"Bash(rustc *)",
	"Bash(mvn *)",
	"Bash(gradle *)",
	"Bash(make *)",
	// Shell utilities (needed for any language)
	"Bash(ls *)",
	"Bash(cat *)",
	"Bash(echo *)",
	"Bash(mkdir *)",
	"Bash(cp *)",
	"Bash(mv *)",
	"Bash(rm *)",
	"Bash(touch *)",
	"Bash(chmod *)",
	"Bash(find *)",
	"Bash(grep *)",
	"Bash(sed *)",
	"Bash(awk *)",
	// Windows PowerShell
	"PowerShell(git *)",
	"PowerShell(go *)",
	`PowerShell(.\golem*)`,
	// File operations
	"Edit(**)",
	"Write(**)",
}

func settingsJSON(allowList []string) string {
	doc := struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}{}
	doc.Permissions.Allow = allowList
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}

func GenerateClaudeCodeCommands(repoRoot string) error {
	dir := filepath.Join(repoRoot, ".claude", "commands", "golem")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "new-ticket.md"), []byte(newTicketSkill), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "tickets.md"), []byte(ticketsSkill), 0o644)
}

func (c ClaudeCode) WorktreeSetup(worktreePath string) error {
	dir := filepath.Join(worktreePath, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	settings := settingsJSON(claudeCodeAllowList)
	return os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644)
}

func (c ClaudeCode) RunAgent(role string, ctx Context) (Result, error) {
	prompt := BuildPrompt(ctx)

	args := []string{"--print"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	cmd := exec.Command("claude", args...)
	cmd.Dir = c.RepoRoot
	cmd.Stdin = strings.NewReader(prompt)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("claude --print failed for role %s: %w\n%s", role, err, stderr.String())
	}
	return Result{Output: stdout.String(), Model: c.Model}, nil
}
