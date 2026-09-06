# internal/gating

The gating module implements tool-call policy enforcement for Golem agent roles. It exposes a single Evaluate function that acts as the enforcement point (analogous to a PreToolUse hook) before any tool call is permitted to execute. It blocks a hardcoded set of dangerous command prefixes (git push, gh pr create/comment, chmod 777, eval, source), rejects common obfuscation vectors (pipe-to-shell, base64 decode), restricts network calls via curl/wget to an explicit allowlist, and enforces worktree-containment for write_file operations when the policy requires it.

## Functions

- Evaluate
- TestEvaluateDeniesGitPush
- TestEvaluateDeniesGhPrCreate
- TestEvaluateDeniesObfuscationVectors
- TestEvaluateAllowsWritesInsideWorktree
- TestEvaluateDeniesWritesOutsideWorktree
- TestEvaluateAllowsExplicitlyAllowedNetworkHost
- TestEvaluateDeniesEvalAndSource

## Types

- Decision

## Imports

path/filepath, strings, github.com/leonp92/golem/internal/config, testing
