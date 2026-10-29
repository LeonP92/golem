package agentrunner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leonp92/golem/internal/agentenv"
	"github.com/leonp92/golem/internal/config"
)

// toolSession owns a per-invocation policy snapshot, outside the repository.
// Native hooks are guardrails. They cannot isolate arbitrary shell processes.
type toolSession struct {
	dir, root, command string
	noTools            bool
}

func prepareTools(root string, noTools bool) (*toolSession, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	policy := config.PolicyConfig{AllowWorktreeOnly: true}
	path := filepath.Join(abs, ".golem", "config.yaml")
	// Resolve the host's local config for ticket paths that do not contain it.
	ticketRoot := abs
	if filepath.Base(abs) == "worktree" {
		ticketRoot = filepath.Dir(abs)
	}
	if filepath.Base(filepath.Dir(ticketRoot)) == "tickets" && filepath.Base(filepath.Dir(filepath.Dir(ticketRoot))) == ".golem" {
		repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(ticketRoot)))
		path = filepath.Join(repoRoot, ".golem", "config.yaml")
	}

	if _, err := os.Stat(path); err == nil {
		cfg, err := config.Load(path)
		if err != nil {
			return nil, err
		}
		policy = cfg.ToolPolicy
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "golem-tool-policy-")
	if err != nil {
		return nil, err
	}
	s := &toolSession{dir: dir, root: abs, noTools: noTools}
	if noTools {
		s.root = dir
	}
	// A dropped agent account must read these non-secret files.
	if err := os.Chmod(dir, 0755); err != nil {
		s.close()
		return nil, err
	}
	data, err := json.Marshal(policy)
	if err != nil {
		s.close()
		return nil, err
	}
	snapshot := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(snapshot, data, 0644); err != nil {
		s.close()
		return nil, err
	}
	binary, err := golemBinary(os.Executable, exec.LookPath)
	if err != nil {
		s.close()
		return nil, err
	}
	s.command = shellQuote(binary) + " policy-check --policy " + shellQuote(snapshot) + " --root " + shellQuote(s.root)
	if noTools {
		s.command += " --no-tools"
	}
	s.command += `; status=$?; if [ "$status" -ne 0 ]; then exit 2; fi`
	return s, nil
}

// golemBinary picks the executable the hook runs. The running binary wins when it is golem itself, so a hook never calls an older golem on PATH that lacks
// policy-check; otherwise golem must be found on PATH, because a missing hook binary would deny every tool call with no clear reason.
func golemBinary(executable func() (string, error), lookPath func(string) (string, error)) (string, error) {
	if self, err := executable(); err == nil {
		base := strings.ToLower(filepath.Base(self))
		if base == "golem" || base == "golem.exe" {
			return self, nil
		}
	}
	found, err := lookPath("golem")
	if err != nil {
		return "", fmt.Errorf("the golem binary is needed to enforce the tool policy but was not found on PATH: %w", err)
	}
	return found, nil
}

func (s *toolSession) close()    { _ = os.RemoveAll(s.dir) }
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (s *toolSession) hooks() map[string]any {
	return map[string]any{"PreToolUse": []any{map[string]any{"matcher": ".*", "hooks": []any{map[string]any{"type": "command", "command": s.command}}}}}
}
func (s *toolSession) claude(argv []string) ([]string, error) {
	// disableAllHooks=false wins over project and local settings that would switch the policy hook off.
	data, err := json.Marshal(map[string]any{"hooks": s.hooks(), "disableAllHooks": false})
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, "settings.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return nil, err
	}
	argv = append(argv, "--settings", path)
	if s.noTools {
		argv = append(argv, "--tools", "", "--disallowedTools", "mcp__*", "--setting-sources", "", "--strict-mcp-config")
	}
	return argv, nil
}
func (s *toolSession) codex(argv []string) ([]string, error) {
	// Trust only this generated hook by its normalized declaration hash.
	// Never bypass trust for unrelated user/project/plugin hooks.
	encoded, _ := json.Marshal(s.command)
	hook := `hooks.PreToolUse=[{matcher=".*",hooks=[{type="command",command=` + string(encoded) + `}]}]`
	// Keep stdin's trailing dash last.
	at := len(argv)
	if at > 0 && argv[at-1] == "-" {
		at--
	}
	out := append([]string{}, argv[:at]...)
	identity := map[string]any{"event_name": "pre_tool_use", "matcher": ".*", "hooks": []any{map[string]any{"type": "command", "command": s.command, "timeout": 600, "async": false}}}
	normalized, err := json.Marshal(identity)
	if err != nil {
		return nil, err
	}
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(normalized))
	source := "/<session-flags>/config.toml:pre_tool_use:0:0"
	if runtime.GOOS == "windows" {
		source = `C:\<session-flags>\config.toml:pre_tool_use:0:0`
	}
	key, _ := json.Marshal(source)
	value, _ := json.Marshal(hash)
	state := `hooks.state={` + string(key) + `={enabled=true,trusted_hash=` + string(value) + `}}`
	out = append(out, "-c", hook, "-c", state)
	return append(out, argv[at:]...), nil
}
func (s *toolSession) openCodeEnv(existing string) ([]string, error) {
	// A private plugin intercepts calls before execution, including tools that
	// don't have a native permission entry. It delegates decisions to Go.
	plugin := `import { spawnSync } from "node:child_process";
export const GolemPolicy = async () => ({
 "tool.execute.before": async (input, output) => {
  const args = output.args || {};
  const event = {tool_name: input.tool, cwd: args.workdir || process.cwd(), tool_input: {command: args.command, file_path: args.filePath || args.path, patchText: args.patchText, url: args.url}};
  const result = spawnSync("sh", ["-c", COMMAND], {input: JSON.stringify(event), encoding: "utf8"});
  if (result.error || result.status !== 0) throw new Error(result.stderr || "Golem tool policy unavailable");
 }
});
`
	command, _ := json.Marshal(s.command)
	plugin = strings.ReplaceAll(plugin, "COMMAND", string(command))
	path := filepath.Join(s.dir, "policy.mjs")
	if err := os.WriteFile(path, []byte(plugin), 0644); err != nil {
		return nil, err
	}
	cfg := map[string]any{}
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &cfg); err != nil {
			return nil, fmt.Errorf("cannot merge OPENCODE_CONFIG_CONTENT tool policy: %w", err)
		}
	}
	plugins := []any{}
	if prior, ok := cfg["plugin"]; ok {
		var valid bool
		plugins, valid = prior.([]any)
		if !valid {
			return nil, fmt.Errorf("unsupported OpenCode plugin configuration")
		}
	}
	cfg["plugin"] = append(plugins, fileURL(path))
	if s.noTools {
		cfg["permission"] = map[string]string{"*": "deny"}
		cfg["agent"] = map[string]any{"build": map[string]any{"permission": map[string]string{"*": "deny"}}}
		cfg["default_agent"] = "build"
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	// Explicit configuration is higher precedence than repository config.
	return []string{"OPENCODE_CONFIG_CONTENT=" + string(data)}, nil
}

// Merge against exactly the effective environment the backend process receives,
// including operator overrides, rather than inheriting an unrelated raw value.
func openCodeContent(opts Options) string {
	var content string
	for _, pair := range append(agentenv.Environ(), agentenv.FilterPairs(opts.Env)...) {
		name, value, _ := strings.Cut(pair, "=")
		if name == "OPENCODE_CONFIG_CONTENT" {
			content = value
		}
	}
	return content
}

// fileURL renders an absolute path as a file URL. A Windows volume path needs
// the extra slash (file:///C:/x), otherwise C: parses as the host.
func fileURL(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}
