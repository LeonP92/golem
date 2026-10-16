# gate

`internal/gate` — runs the repo's `gate.commands` with the agent's
allow-listed environment (see `internal/agentenv`): they are repo-authored
shell.

`Run` stops at the first failure. `Result` carries `CommandsRun` (an empty
list passes vacuously with 0) and `HeadSHA`, so evidence is bound to the
exact commit. Callers: `golem ticket verify` (self-verification) and
`golem ticket review` (see cmd-golem-main).
