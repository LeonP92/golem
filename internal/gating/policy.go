package gating

import (
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/config"
)

type Decision struct {
	Allowed bool
	Reason  string
}

// This is substring matching, not a parser — a cheap heuristic, not a
// real security boundary. "curl x |  sh" (two spaces) or an equivalent
// respacing bypasses it. It raises the bar against casual/accidental
// cases; it does not stop a determined adversarial prompt.
var obfuscationSubstrings = []string{
	"| sh",
	"|sh",
	"base64 -d",
	"base64 --decode",
}

// Evaluate decides whether a role may execute a tool call. This is the
// enforcement point every backend adapter's PreToolUse-equivalent hook
// calls before letting a tool call through. Shell checks are guardrails, not isolation.
func Evaluate(policy config.PolicyConfig, worktreePath, tool string, args []string) Decision {
	joined := strings.Join(args, " ")

	segments := commandSegments(tool, args)
	for _, segment := range segments {
		for _, prefix := range deniedCommands {
			if matchesPrefix(segment, prefix) {
				return Decision{Allowed: false, Reason: "denied command: " + strings.Join(prefix, " ")}
			}
		}
	}

	for _, bad := range obfuscationSubstrings {
		if strings.Contains(joined, bad) {
			return Decision{Allowed: false, Reason: "denied obfuscation vector: " + bad}
		}
	}

	if strings.Contains(joined, "curl") || strings.Contains(joined, "wget") {
		for _, segment := range segments {
			for _, token := range segment {
				if host := extractHost(token); host != "" && !hostAllowed(policy.AllowNetwork, host) {
					return Decision{Allowed: false, Reason: "denied network call to non-allow-listed host: " + host}
				}
			}
		}
	}

	if tool == "write_file" && policy.AllowWorktreeOnly && len(args) > 0 {
		target := args[0]
		if !withinWorktree(worktreePath, target) {
			return Decision{Allowed: false, Reason: "denied write outside worktree: " + target}
		}
	}

	return Decision{Allowed: true}
}

func matchesPrefix(args, prefix []string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i, p := range prefix {
		if args[i] != p {
			return false
		}
	}
	return true
}

func withinWorktree(worktreePath, target string) bool {
	rel, err := filepath.Rel(resolveExisting(worktreePath), resolveExisting(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func extractHost(command string) string {
	for _, token := range strings.Fields(command) {
		if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
			rest := strings.TrimPrefix(strings.TrimPrefix(token, "https://"), "http://")
			if idx := strings.IndexAny(rest, "/:"); idx != -1 {
				rest = rest[:idx]
			}
			return rest
		}
	}
	return ""
}

func hostAllowed(allowed []string, host string) bool {
	for _, a := range allowed {
		if a == host {
			return true
		}
	}
	return false
}

// resolveExisting follows symlinks on the deepest part of the path that exists, so a link inside the worktree that points outside cannot
// carry a write out and a worktree reached through a link (macOS /var) still matches itself. A path that does not exist yet keeps its tail.
func resolveExisting(path string) string {
	path = filepath.Clean(path)
	tail := ""
	for {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Join(real, tail)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return filepath.Join(path, tail)
		}
		tail = filepath.Join(filepath.Base(path), tail)
		path = parent
	}
}
