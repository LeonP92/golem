package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leonp92/golem/internal/evidence"
	"github.com/leonp92/golem/internal/gating"
	"github.com/leonp92/golem/internal/models"
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

	return mergeClaudeCodeSettings(filepath.Join(repoRoot, ".claude", "settings.json"))
}

func init() {
	Register("claude-code", func(o Options) Adapter { return ClaudeCode{opts: o} })
}

// ClaudeCode runs the `claude` CLI with the host's own authentication.
type ClaudeCode struct{ opts Options }

func (c ClaudeCode) Name() string { return "claude-code" }

// ReservedArgs includes -p because `claude` treats it as --print.
func (c ClaudeCode) ReservedArgs() []string {
	return []string{"--print", "-p", "--model", "--settings", "--tools", "--disallowedTools", "--dangerously-skip-permissions", "--setting-sources", "--strict-mcp-config"}
}

func (c ClaudeCode) argv(model string) ([]string, error) {
	cmd := c.opts.Command
	if cmd == "" {
		cmd = "claude"
	}
	args := []string{cmd, "--print"}
	if model != "" {
		if !models.ValidModelID(model) {
			return nil, fmt.Errorf("invalid model id %q", model)
		}
		args = append(args, "--model", model)
	}
	return append(args, c.opts.ExtraArgs...), nil
}

// GenerateArtifacts writes the Claude Code subagent definitions and commands.
func (c ClaudeCode) GenerateArtifacts(repoRoot string, roleContent map[string]string) error {
	if err := GenerateClaudeCodeArtifacts(repoRoot, roleContent); err != nil {
		return err
	}
	return GenerateClaudeCodeCommands(repoRoot)
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
   ` + "`golem ticket check-bloat`" + ` (rule-based, no LLM call).
7. Once the last plan step is committed, run the two
   ` + "`golem observer dispatch`" + ` calls and ` + "`golem graph update`" + ` per
   ` + "`.golem/roles/developer.md`" + `. If either observer emits a BLOCKER,
   address it and re-run the failing observer.
8. When observers pass, run
   ` + "`golem ticket review --ticket <id>`" + ` and report the result to the
   human.
9. Once the human approves the reviewed work, run
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
	// File operations. Anchored to the settings file's directory (the repo
	// or worktree root): an unanchored "Edit(**)" is relative to the session's
	// current directory, so an agent that cd's into its worktree to read code
	// loses write access to the ticket directory above it, and spec.md or
	// plan.md writes there need a permission prompt --print cannot answer.
	"Edit(/**)",
}

// bashDeny blocks destructive or network-exfiltrating commands that agents
// have no legitimate need for inside a repo worktree; each backend wraps these
// patterns in its own rule syntax.
var bashDeny = func() []string {
	var rules []string
	for _, prefix := range gating.DeniedCommands() {
		command := strings.Join(prefix, " ")
		rules = append(rules, command, command+" *")
	}
	return rules
}()

var claudeCodeDenyList = func() []string {
	rules := make([]string, len(bashDeny))
	for i, r := range bashDeny {
		rules[i] = "Bash(" + r + ")"
	}
	return rules
}()

func union(base, additions []string) []string {
	seen := make(map[string]bool, len(base))
	for _, e := range base {
		seen[e] = true
	}
	result := append([]string(nil), base...)
	for _, e := range additions {
		if !seen[e] {
			result = append(result, e)
		}
	}
	return result
}

// mergeClaudeCodeSettings adds golem's missing allow/deny rules to the
// settings file at path (used by GenerateClaudeCodeArtifacts and
// WorktreeSetup). It merges, never drops existing fields, and leaves the file
// untouched when nothing is missing.
func mergeClaudeCodeSettings(path string) error {
	existing, err := os.ReadFile(path) //nolint:gosec // the repository's own settings file
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Raw maps, so fields this code does not know about pass through as-is.
	existing = bytes.TrimPrefix(existing, []byte("\xef\xbb\xbf"))
	var raw map[string]json.RawMessage
	if len(existing) > 0 && (json.Unmarshal(existing, &raw) != nil || raw == nil) {
		log.Printf("agentrunner: %s is not a JSON object; using golem's defaults", path)
		raw = nil
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	var perms map[string]json.RawMessage
	if p, ok := raw["permissions"]; ok && json.Unmarshal(p, &perms) != nil {
		perms = nil
	}
	if perms == nil {
		perms = map[string]json.RawMessage{}
	}

	allow, allowAdded := mergeRuleList(perms["allow"], claudeCodeAllowList)
	deny, denyAdded := mergeRuleList(perms["deny"], claudeCodeDenyList)
	if !allowAdded && !denyAdded {
		return nil // already grants and denies everything golem needs
	}

	if perms["allow"], err = marshalJSON(allow); err != nil {
		return err
	}
	if perms["deny"], err = marshalJSON(deny); err != nil {
		return err
	}
	if raw["permissions"], err = marshalJSON(perms); err != nil {
		return err
	}
	compact, err := marshalJSON(raw)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out.Bytes(), 0o644) //nolint:gosec // settings are not secret
}

// mergeRuleList unions golem's rules into an existing permissions list and
// reports whether the list changed. A list that is absent or is not an array
// of strings counts as empty.
func mergeRuleList(existing json.RawMessage, golem []string) ([]string, bool) {
	var have []string
	if len(existing) > 0 && json.Unmarshal(existing, &have) != nil {
		return append([]string(nil), golem...), true
	}
	merged := union(have, golem)
	return merged, len(existing) == 0 || len(merged) != len(have)
}

// marshalJSON is json.Marshal without HTML escaping, so a rule such as
// "Bash(a && b)" is written as typed rather than with each & escaped to a
// \u-sequence.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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

// WorktreeSetup merges golem's permissions into the worktree's
// .claude/settings.json (the repository's committed file, as the checkout
// just produced it) rather than replacing it.
func (c ClaudeCode) WorktreeSetup(worktreePath string) error {
	if err := mergeClaudeCodeSettings(filepath.Join(worktreePath, ".claude", "settings.json")); err != nil {
		return err
	}
	skipWorktree(worktreePath, ".claude/settings.json")
	return nil
}

func (c ClaudeCode) RunAgent(ctx context.Context, role string, in Context, model string) (Result, error) {
	argv, err := c.argv(model)
	if err != nil {
		return Result{}, err
	}
	session, err := prepareTools(toolRoot(ctx, c.opts.RepoRoot), role == "diagnoser")
	if err != nil {
		return Result{}, err
	}
	defer session.close()
	argv, err = session.claude(argv)
	if err != nil {
		return Result{}, err
	}
	cwd := c.opts.RepoRoot
	if session.noTools {
		cwd = session.root
	}
	cmd := agentCmd(ctx, c.opts, cwd, argv...)
	cmd.Stdin = strings.NewReader(BuildPrompt(in))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := runAgentCmd(cmd); err != nil {
		// BOTH streams. Claude Code reports most of what goes wrong on
		// stdout — "Not logged in · Please run /login", an unreadable
		// setting, a refused tool — and this used to report only stderr, so
		// a failure surfaced as an exit status next to whatever unrelated
		// noise happened to be on the other stream. In the case that
		// prompted this, stderr held a single line about a SessionEnd hook
		// while the reason sat unread in stdout.
		return Result{}, fmt.Errorf("claude --print failed for role %s: %w%s%s",
			role, err, labelled("stderr", evidence.Clean(stderr.String())), labelled("stdout", evidence.Clean(stdout.String())))
	}
	return Result{Output: evidence.Clean(stdout.String()), Model: model}, nil
}

func (c ClaudeCode) RunPhase(ctx context.Context, dir, prompt string, out io.Writer, model string) error {
	argv, err := c.argv(model)
	if err != nil {
		return err
	}
	session, err := prepareTools(toolRoot(ctx, dir), false)
	if err != nil {
		return err
	}
	defer session.close()
	argv, err = session.claude(argv)
	if err != nil {
		return err
	}
	cmd := agentCmd(ctx, c.opts, dir, argv...)
	if err := runCleaned(cmd, prompt, out); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return nil
}

// maxCapturedStream bounds how much of a failed agent's output reaches the
// error. Claude's stdout can be the whole of a long answer; the tail is what
// carries the failure.
const maxCapturedStream = 2000

// labelled renders a captured stream for an error message, tail-first when it
// is long, and renders nothing at all when the stream is empty so an error
// does not carry a heading with nothing under it.
func labelled(name, out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if len(out) > maxCapturedStream {
		out = "…(truncated; last " + strconv.Itoa(maxCapturedStream) + " bytes)…\n" +
			out[len(out)-maxCapturedStream:]
	}
	return "\n--- " + name + " ---\n" + out
}

// skipWorktree keeps golem's edits to a tracked file out of `git add -A`;
// it fails harmlessly when the file is untracked.
func skipWorktree(worktreePath, rel string) {
	_ = exec.Command("git", "-C", worktreePath, "update-index", "--skip-worktree", rel).Run() //nolint:gosec
}
