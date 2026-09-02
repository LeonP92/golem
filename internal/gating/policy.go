package gating

import (
	"path/filepath"
	"strings"

	"github.com/leonpham/golem/internal/config"
)

type Decision struct {
	Allowed bool
	Reason  string
}

var deniedCommandPrefixes = [][]string{
	{"git", "push"},
	{"gh", "pr", "create"},
	{"gh", "pr", "comment"},
	{"chmod", "777"},
	{"eval"},
	{"source"},
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
// calls before letting a tool call through (spec: Tool-Call Gating).
func Evaluate(policy config.PolicyConfig, worktreePath, tool string, args []string) Decision {
	joined := strings.Join(args, " ")

	// Prepend tool to args for prefix matching
	fullArgs := append([]string{tool}, args...)

	for _, prefix := range deniedCommandPrefixes {
		if matchesPrefix(fullArgs, prefix) {
			return Decision{Allowed: false, Reason: "denied command: " + strings.Join(prefix, " ")}
		}
	}

	for _, bad := range obfuscationSubstrings {
		if strings.Contains(joined, bad) {
			return Decision{Allowed: false, Reason: "denied obfuscation vector: " + bad}
		}
	}

	if strings.Contains(joined, "curl") || strings.Contains(joined, "wget") {
		if host := extractHost(joined); host != "" && !hostAllowed(policy.AllowNetwork, host) {
			return Decision{Allowed: false, Reason: "denied network call to non-allow-listed host: " + host}
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
	// Check if prefix matches anywhere in the args sequence
	for start := 0; start <= len(args)-len(prefix); start++ {
		matches := true
		for i, p := range prefix {
			if args[start+i] != p {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func withinWorktree(worktreePath, target string) bool {
	rel, err := filepath.Rel(worktreePath, target)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..")
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
