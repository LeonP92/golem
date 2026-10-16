# cmd/golem/main.go

Entry point and command dispatcher for the golem CLI. Routes top-level commands to their handler functions via a `commandTable` map. Added `golem help` / `golem help <command>` in the graph-subsystem ticket: iterates `commandGroups` (ordered groups by area) to print one-line descriptions, or delegates to the target command's `--help` flag output for per-command usage.

`golem models` (`modelsDispatch`) is the model command group: `list` prints a
backend's tiers, models and per-stage default models; `probe` checks each
catalog model against the vendor CLI and prints the verified `backend:` block.
Both read a `shem.yaml` via `--config` or a standalone document via
`--backend-config`.

`golem ticket new` accepts `--base <ref>` / `--base-sha <sha>` (worktree
created from the resolved exact commit, recorded as `branch_base_sha`;
without them it uses `HEAD`) and repeatable `--accept <cmd>` (acceptance
commands stored on the ticket and run by the gate after `gate.commands`).

`golem ticket verify` is the worker's self-verification: runs the ticket's
gate and records a `self-verification` entry bound to the head. Exit 0
pass, 2 a check failed, 1 could not run.

`golem ticket review` runs the gate first and logs `gate evidence:` (head,
commands run, the command list and output), then the reviewer, which gets
the log and the branch diff since `branch_base_sha`. Ready-for-review
needs a passing gate AND the reviewer's `VERDICT: PASS`; a FAIL, missing or
unreadable verdict parks the ticket in needs-attention. The verdict
contract (`reviewerVerdictContract`) is appended to every invocation, so
old or customized reviewer.md files keep working. Observers that find
nothing log a `no findings` STATUS for the commit, so silence is visible
evidence.
