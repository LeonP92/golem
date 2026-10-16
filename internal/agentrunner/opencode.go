package agentrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

// jsonField is one key of a JSON object, kept in document order.
type jsonField struct {
	Key   string
	Value json.RawMessage
}

// orderedObject decodes a JSON object without losing key order, which
// opencode's last-match-wins permission rules depend on.
func orderedObject(raw []byte) ([]jsonField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not a JSON object")
	}
	var out []jsonField
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = setField(out, tok.(string), v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON object")
	}
	return out, nil
}

// setField replaces key's value in place, or appends it.
func setField(fields []jsonField, key string, v json.RawMessage) []jsonField {
	for i := range fields {
		if fields[i].Key == key {
			fields[i].Value = v
			return fields
		}
	}
	return append(fields, jsonField{key, v})
}

func getField(fields []jsonField, key string) (json.RawMessage, bool) {
	for _, f := range fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func encodeObject(fields []jsonField) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := marshalJSON(f.Key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(f.Value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func jsonString(s string) json.RawMessage {
	out, _ := marshalJSON(s)
	return out
}

// mergeOpenCodePermission merges golem's rules into an operator's
// permission value (nil when absent).
//
// Opencode evaluates rules by pattern with the LAST match winning
// (https://opencode.ai/docs/permissions), so bash order is load-bearing:
// golem's "deny" catch-all and allows, then the operator's entries (theirs
// win over golem's), then golem's denies, which always win — the same
// precedence claude gives a deny. Never "ask": `opencode run` auto-rejects
// an ask and ends the whole run, while a deny is returned to the model as a
// tool error, as claude --print does for an unlisted command. Other
// permission keys keep the operator's value.
func mergeOpenCodePermission(existing json.RawMessage) (json.RawMessage, error) {
	var perm []jsonField
	if len(existing) > 0 {
		var err error
		if perm, err = orderedObject(existing); err != nil {
			return nil, fmt.Errorf("permission: %w", err)
		}
	}
	bash := []jsonField{{"*", jsonString("deny")}}
	for _, g := range opencodeBashAllow {
		bash = setField(bash, g, jsonString("allow"))
	}
	if v, ok := getField(perm, "bash"); ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			bash = setField(bash, "*", v) // a bare string is the catch-all
		} else {
			ops, err := orderedObject(v)
			if err != nil {
				return nil, fmt.Errorf("permission.bash: %w", err)
			}
			for _, f := range ops {
				bash = setField(bash, f.Key, f.Value)
			}
		}
	}
	for _, g := range opencodeBashDeny {
		for i := range bash {
			if bash[i].Key == g {
				bash = append(bash[:i], bash[i+1:]...)
				break
			}
		}
		bash = append(bash, jsonField{g, jsonString("deny")})
	}
	perm = setField(perm, "bash", encodeObject(bash))
	// edit mirrors claude's Edit(**); webfetch is denied as claude denies
	// an unlisted WebFetch. The rest default to "ask" in opencode, so pin
	// them to deny for the reason above.
	for _, d := range []jsonField{
		{"edit", jsonString("allow")}, {"webfetch", jsonString("deny")},
		{"external_directory", jsonString("deny")}, {"doom_loop", jsonString("deny")},
	} {
		if _, ok := getField(perm, d.Key); !ok {
			perm = append(perm, d)
		}
	}
	return encodeObject(perm), nil
}

// opencodePermissionJSON is golem's permission block on its own.
func opencodePermissionJSON() json.RawMessage {
	out, _ := mergeOpenCodePermission(nil)
	return out
}

// writeOpenCodeConfig merges golem's permission block into dir's
// opencode.json, keeping every other key and the operator's own rules. A
// file encoding/json cannot read (opencode also accepts comments) is left
// alone rather than replaced, and an unchanged result is not rewritten.
func writeOpenCodeConfig(dir string) error {
	configPath := filepath.Join(dir, "opencode.json")
	existing, err := os.ReadFile(configPath) //nolint:gosec // path inside the worked repo
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	existing = bytes.TrimPrefix(existing, []byte("\xef\xbb\xbf"))
	var top []jsonField
	if len(bytes.TrimSpace(existing)) > 0 {
		if top, err = orderedObject(existing); err != nil {
			log.Printf("agentrunner: leaving %s alone (%v); golem's permission rules are not applied", configPath, err)
			return nil
		}
	}
	prev, _ := getField(top, "permission")
	perm, err := mergeOpenCodePermission(prev)
	if err != nil {
		log.Printf("agentrunner: leaving %s alone (%v); golem's permission rules are not applied", configPath, err)
		return nil
	}
	top = setField(top, "permission", perm)
	var out bytes.Buffer
	if err := json.Indent(&out, encodeObject(top), "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	if bytes.Equal(out.Bytes(), existing) {
		return nil
	}
	return os.WriteFile(configPath, out.Bytes(), 0o644) //nolint:gosec // not secret
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
	if err := writeOpenCodeConfig(worktreePath); err != nil {
		return err
	}
	skipWorktree(worktreePath, "opencode.json")
	return nil
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
