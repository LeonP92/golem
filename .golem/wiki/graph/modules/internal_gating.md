# internal/gating

The gating module implements tool-call policy enforcement for Golem agent roles. It exposes a single Evaluate function that acts as the enforcement point (analogous to a PreToolUse hook) before any tool call is permitted to execute. It blocks a hardcoded set of dangerous command prefixes (git push, gh pr create/comment, chmod 777, eval, source), rejects common obfuscation vectors (pipe-to-shell, base64 decode), restricts network calls via curl/wget to an explicit allowlist, and enforces worktree-containment for write_file operations when the policy requires it.

## Functions

- func Evaluate(policy config.PolicyConfig, worktreePath, tool string, args []string) Decision — decides whether a role may execute a tool call given the active policy

## Types

- Decision — holds the allow/deny outcome and a human-readable reason string

## Imports

internal/config
