package gating

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/evidence"
)

// Call is the normalized tool event consumed by every adapter hook.
type Call struct {
	CWD     string
	Tool    string
	Command string
	Patch   string
	Path    string
	URL     string
}

// DecodeCall accepts the vendor envelope at the boundary, not in the policy.
func DecodeCall(r io.Reader) (Call, error) {
	var event struct {
		CWD   string `json:"cwd"`
		Tool  string `json:"tool_name"`
		Input struct {
			Command   string `json:"command"`
			FilePath  string `json:"file_path"`
			Path      string `json:"path"`
			Patch     string `json:"patch"`
			PatchText string `json:"patchText"`
			Notebook  string `json:"notebook_path"`
			URL       string `json:"url"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 2<<20)).Decode(&event); err != nil {
		return Call{}, err
	}
	if event.Tool == "" {
		return Call{}, fmt.Errorf("tool event has no tool_name")
	}
	path := event.Input.FilePath
	if path == "" {
		path = event.Input.Path
	}
	if path == "" {
		path = event.Input.Notebook
	}
	patch := event.Input.Patch
	if patch == "" {
		patch = event.Input.PatchText
	}
	if isPatchTool(event.Tool) && patch == "" {
		patch = event.Input.Command
	}
	return Call{Tool: event.Tool, Command: event.Input.Command, Path: path, CWD: event.CWD, Patch: patch, URL: event.Input.URL}, nil
}

// Check normalizes names and shell token boundaries before Evaluate. It is an
// accident-prevention guardrail, not an adversarial shell or network sandbox.
func Check(policy config.PolicyConfig, root string, call Call) Decision {
	if isPatchTool(call.Tool) {
		found := false
		for _, line := range strings.Split(call.Patch, "\n") {
			for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
				if path, ok := strings.CutPrefix(line, prefix); ok {
					found = true
					d := Check(policy, root, Call{Tool: "write_file", Path: strings.TrimSpace(path), CWD: call.CWD})
					if !d.Allowed {
						return d
					}
				}
			}
		}
		if !found {
			return Decision{Reason: "unsupported patch input"}
		}
		return Decision{Allowed: true}
	}
	if strings.EqualFold(call.Tool, "webfetch") {
		return checkFetch(policy, call.URL)
	}
	if call.Path != "" {
		if evidence.SensitivePath(call.Path) {
			return Decision{Reason: "live env/key files are unavailable to agents; use test fixtures"}
		}
		target := call.Path
		if !filepath.IsAbs(target) {
			cwd := call.CWD
			if cwd == "" {
				cwd = root
			}
			target = filepath.Join(cwd, target)
		}
		switch strings.ToLower(call.Tool) {
		case "write", "edit", "multiedit", "notebookedit", "write_file", "apply_patch":
			return Evaluate(policy, root, "write_file", []string{target})
		}
	}
	if call.Command != "" {
		for _, segment := range commandSegments("bash", []string{call.Command}) {
			for _, token := range segment {
				if evidence.SensitivePath(token) {
					return Decision{Reason: "live env/key files are unavailable to agents; use test fixtures"}
				}
			}
		}
		return Evaluate(policy, root, "bash", []string{call.Command})
	}
	switch strings.ToLower(call.Tool) {
	case "bash", "exec_command", "shell", "write", "edit", "multiedit", "notebookedit", "write_file", "apply_patch":
		return Decision{Reason: "unsupported or incomplete tool input"}
	// Read-only or self-gated tools: a shell or subagent's own tool calls pass through this same hook.
	case "read", "read_file", "glob", "grep", "list", "ls", "todowrite", "todoread", "update_plan", "bashoutput", "killshell", "task",
		"agent", "skill", "toolsearch", "taskcreate", "taskupdate", "tasklist", "taskget", "askuserquestion", "exitplanmode", "enterplanmode", "killbash":
		return Decision{Allowed: true}
	default:
		return Decision{Reason: "tool policy does not support this tool: " + call.Tool}
	}
}

func isPatchTool(tool string) bool {
	return strings.EqualFold(tool, "apply_patch") || strings.EqualFold(tool, "patch")
}

// checkFetch applies the same allow_network host list as the shell curl check.
func checkFetch(policy config.PolicyConfig, rawURL string) Decision {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return Decision{Reason: "unsupported fetch url: " + rawURL}
	}
	if !hostAllowed(policy.AllowNetwork, u.Hostname()) {
		return Decision{Reason: "denied network call to non-allow-listed host: " + u.Hostname()}
	}
	return Decision{Allowed: true}
}
