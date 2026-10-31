# gating

`internal/gating` — the tool-call policy every backend hook enforces.
`golem policy-check` decodes the vendor hook event (`DecodeCall`) and calls
`Check`; exit 2 blocks the call.

- `DeniedCommands` is the one deny list, also projected into the Claude,
  OpenCode and Codex native rules. A command is denied only when it starts a
  command: the first word of the line or of the text after `;`, `&&`, `||`,
  `|`, `(`, a backtick or `$(` (`commandSegments`), and inside `sh -c "..."`.
  Words in arguments (`git commit -m "drop sudo"`) are not matched.
  The obfuscation substrings (`| sh`, `base64 -d`) are matched outside quotes and
  inside `sh -c` scripts, so a quoted pattern like `rg "a|shell"` is allowed.
- `Check` maps tool names (Claude, Codex, OpenCode) to rules: writes
  (`Write`, `Edit`, `MultiEdit`, `NotebookEdit`, `patch`/`apply_patch`) must stay
  inside the worktree; `WebFetch` hosts must be in `allow_network`; shell
  commands go through `Evaluate`; read-only and self-gated tools pass; anything
  unknown is denied.
- A guardrail, not a sandbox: shell writes outside the worktree are not blocked.
