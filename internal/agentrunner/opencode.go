package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/models"
)

// OpenCode runs SST's opencode CLI (`opencode run`) headless.
//
// This is the adapter for OpenAI-compatible endpoints: opencode treats
// custom providers as first-class config ("npm": "@ai-sdk/openai-compatible"
// plus a baseURL), where codex removed its chat-completions wire API in
// February 2026. The invocation mirrors the other adapters: one process per
// role, the prompt on stdin — `opencode run` reads a piped stdin as the
// message (verified against opencode 1.19.2), so untrusted ticket text
// stays out of the process list.
//
// Auth is the operator's: a saved credential from `opencode auth login`
// (stored in ~/.local/share/opencode/auth.json), or a provider env var such
// as ANTHROPIC_API_KEY or OPENAI_API_KEY — the default catalog's
// anthropic/* models work with the same key a claude-code operator already
// has.
type OpenCode struct{ opts Options }

func init() {
	Register("opencode", func(o Options) Adapter { return OpenCode{opts: o} })
}

func (o OpenCode) Name() string { return "opencode" }

// ReservedArgs are the tokens this adapter sets itself. --format is
// reserved because golem parses stdout as the agent's plain-text answer;
// --agent because roles are inlined into the prompt, not selected as
// opencode agents.
func (o OpenCode) ReservedArgs() []string {
	return []string{"run", "--model", "-m", "--agent", "--format"}
}

// argv builds `opencode run [--model provider/model] [extra]`; the prompt
// arrives on stdin.
func (o OpenCode) argv(model string) ([]string, error) {
	cmd := o.opts.Command
	if cmd == "" {
		cmd = "opencode"
	}
	args := []string{cmd, "run"}
	if model != "" {
		if !models.ValidModelID(model) {
			return nil, fmt.Errorf("invalid model id %q", model)
		}
		args = append(args, "--model", model)
	}
	return append(args, o.opts.ExtraArgs...), nil
}

// opencodeBashAllow is the claude-code adapter's Bash allow list as
// opencode permission globs. Opencode matches against the whole command
// string, so claude's "Bash(git *)" stays "git *". The PowerShell entries
// have no opencode counterpart: opencode's bash tool is the same shell on
// every platform.
var opencodeBashAllow = []string{
	"git *",
	"golem *", "./golem *", "./golem.exe *",
	"go *",
	"python3 *", "python *", "pip *", "pip3 *",
	"npm *", "npx *", "node *",
	"cargo *", "rustc *", "mvn *", "gradle *", "make *",
	"ls *", "cat *", "echo *", "mkdir *", "cp *", "mv *", "rm *",
	"touch *", "chmod *", "find *", "grep *", "sed *", "awk *",
}

// opencodeBashDeny is the same destructive or publishing command set the
// claude adapter denies.
var opencodeBashDeny = []string{
	"git push origin main",
	"git push --force *",
	"git reset --hard *",
	"git rebase *",
	"npm publish *",
	"cargo publish *",
	"twine upload *",
	"python -m twine upload *",
	"sudo *",
}

// opencodePermissionJSON renders golem's permission block for opencode.json.
//
// Opencode permission rules are per tool with "allow" / "ask" / "deny"
// actions, evaluated by pattern match with the LAST match winning
// (https://opencode.ai/docs/permissions). The emission order below is
// therefore load-bearing and deliberately not a Go map: the catch-all
// "ask" first, then the allow globs, then the deny globs so an explicit
// deny beats an overlapping allow. "ask" rather than "deny" for the
// catch-all because opencode run can be given --auto to approve asks while
// still honouring explicit denies — the operator's escape hatch — whereas a
// denied command has none. Without --auto an unlisted command surfaces as
// an unanswered permission request, which fails the run loudly instead of
// silently doing the wrong thing.
func opencodePermissionJSON() json.RawMessage {
	var b strings.Builder
	b.WriteString(`{"bash":{`)
	b.WriteString(`"*":"ask"`)
	writeRules := func(action string, globs []string) {
		for _, g := range globs {
			key, _ := json.Marshal(g)
			fmt.Fprintf(&b, ",%s:%q", key, action)
		}
	}
	writeRules("allow", opencodeBashAllow)
	writeRules("deny", opencodeBashDeny)
	// edit mirrors claude's Edit(**) allowance; webfetch stays at "ask" so
	// network egress is never silently granted.
	b.WriteString(`},"edit":"allow","webfetch":"ask"}`)
	return json.RawMessage(b.String())
}

// writeOpenCodeConfig installs golem's permission block into dir's
// opencode.json, preserving any unrelated top-level keys the operator set,
// the same merge the claude adapter applies to .claude/settings.json.
func writeOpenCodeConfig(dir string) error {
	configPath := filepath.Join(dir, "opencode.json")
	existing, err := os.ReadFile(configPath) //nolint:gosec // path inside the worked repo
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	raw := map[string]json.RawMessage{}
	if len(existing) > 0 {
		_ = json.Unmarshal(existing, &raw)
	}
	raw["permission"] = opencodePermissionJSON()
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, append(out, '\n'), 0o644)
}

// openCodeAgentFrontmatter renders the frontmatter for one projected role.
// Mode subagent: golem selects roles through its own prompt, never through
// opencode's Tab-cycle, so none of these should be a primary agent.
const openCodeAgentFrontmatter = `---
description: Golem %s role
mode: subagent
---

`

// writeOpenCodeAgents projects the neutral role files into opencode's
// agent format under dir's .opencode/agents/, for interactive use and
// parity with the claude adapter's .claude/agents projection. One-shot
// golem invocations still inline the role into the prompt via BuildPrompt.
func writeOpenCodeAgents(dir string, roleContent map[string]string) error {
	agentsDir := filepath.Join(dir, ".opencode", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}
	for role, content := range roleContent {
		frontmatter := fmt.Sprintf(openCodeAgentFrontmatter, role)
		path := filepath.Join(agentsDir, "golem-"+role+".md")
		if err := os.WriteFile(path, []byte(frontmatter+content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// GenerateArtifacts writes opencode.json's permission block and the
// .opencode/agents role files at the repo root.
func (o OpenCode) GenerateArtifacts(repoRoot string, roleContent map[string]string) error {
	if err := writeOpenCodeAgents(repoRoot, roleContent); err != nil {
		return err
	}
	return writeOpenCodeConfig(repoRoot)
}

// WorktreeSetup installs the permission block in a fresh worktree.
func (o OpenCode) WorktreeSetup(worktreePath string) error {
	return writeOpenCodeConfig(worktreePath)
}

// PrepareHost has nothing to do: opencode has no workspace-trust system to
// pre-accept and its auth lives in the user's home, which the deployment
// layer already arranges.
func (o OpenCode) PrepareHost(repoPath string) error { return nil }

func (o OpenCode) RunAgent(ctx context.Context, role string, in Context, model string) (Result, error) {
	argv, err := o.argv(model)
	if err != nil {
		return Result{}, err
	}
	cmd := agentCmd(ctx, o.opts, o.opts.RepoRoot, argv...)
	cmd.Stdin = strings.NewReader(BuildPrompt(in))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := runAgentCmd(cmd); err != nil {
		return Result{}, fmt.Errorf("opencode run failed for role %s: %w%s%s",
			role, err, labelled("stderr", stderr.String()), labelled("stdout", stdout.String()))
	}
	return Result{Output: stdout.String(), Model: model}, nil
}

func (o OpenCode) RunPhase(ctx context.Context, dir, prompt string, out io.Writer, model string) error {
	argv, err := o.argv(model)
	if err != nil {
		return err
	}
	cmd := agentCmd(ctx, o.opts, dir, argv...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stdout, cmd.Stderr = out, out
	if err := runAgentCmd(cmd); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return nil
}
