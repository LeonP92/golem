# cmd/golem/main.go

Entry point and command dispatcher for the golem CLI. Routes top-level commands to their handler functions via a `commandTable` map. Added `golem help` / `golem help <command>` in the graph-subsystem ticket: iterates `commandGroups` (ordered groups by area) to print one-line descriptions, or delegates to the target command's `--help` flag output for per-command usage.

`golem models` (`modelsDispatch`) is the model command group: `list` prints a
backend's tiers, models and per-stage default models; `probe` checks each
catalog model against the vendor CLI and prints the verified `backend:` block.
Both read a `shem.yaml` via `--config` or a standalone document via
`--backend-config`.

`golem ticket new --base <ref>` creates the worktree at the exact commit
<ref> names (recorded as `branch_base_sha`; default `HEAD`); repeatable
`--accept <cmd>` stores acceptance commands the gate runs after
`gate.commands`.

`golem ticket verify` (the worker's self-verification, run for every
ticket unless it opted out) and `golem
ticket review` both run that gate and log its result bound to the head.
Review runs the gate first, then the reviewer on the log and the branch
diff from the merge-base with `--base` (the live base branch; the pinned
`branch_base_sha` only when no `--base` is given, since it goes stale as
siblings merge). A diff over `review.max_diff_bytes` (default 400000) goes to the reviewer as a
per-file summary under a truncation marker, and the review cannot approve it:
the command exits 1 with an environment failure, `review diff too large (N
bytes, cap C)` plus the fix, and parks the ticket in needs-attention. STATUS and BLOCKER bodies over 4KB are collapsed to head and tail in
the reviewer's log slice. `golem log emit`, `ask` and `answer` run from a
linked worktree write to the primary repo's ticket log (git common dir). A reviewer `VERDICT: FAIL` parks the ticket
in needs-attention like a failing gate; a missing verdict does so only for
tickets with acceptance commands. The verdict contract is appended to
every reviewer invocation. Observers that find nothing log a `no findings`
STATUS.

`golem policy-check` is the hook entry point every backend calls for each agent
tool call: it reads a vendor event on stdin, loads `--policy` (JSON) and `--root`,
and exits 2 to block; see gating.
