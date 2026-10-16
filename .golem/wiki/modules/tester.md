# tester

`internal/tester` — the deterministic pieces of the independent tester.

- `Contract` / `ParseVerdict` (tester.go): the role ends its report with
  `TESTER-VERDICT: PASS|FAIL|INCONCLUSIVE` plus `HEAD:`, `BASE:`,
  `SCENARIOS:`, `EVIDENCE:`. The last block wins; missing or unreadable is
  not found (never a pass).
- `Env` / `Command` (sandbox.go): an environment for scenario commands with
  no secrets and no network by default (unshare(1) network namespace where
  available), run as the agent account. The tester stage runs the node's
  acceptance commands through it and hands the results to the agent.

The tester stage itself lives in the shem worker (see shem-worker). It
runs for epic children of runs with `tester_required`, uses the `tester`
role and the `test` stage model. The agent does not run commands; it
judges the sandboxed acceptance results and the code at the head.
