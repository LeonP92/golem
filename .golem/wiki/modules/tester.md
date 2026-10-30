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

The tester stage itself lives in the shem worker (see shem-worker). It is
a ticket-level stage: any ticket created with `tester_required` gets it,
and an epic run composes it by setting that flag on every child at
materialization (auto-merge forces the requirement on at create). It uses
the `tester` role and the `test` stage model. The agent does not run
commands; it judges the sandboxed acceptance results and the code at the
head. An epic child's verdict goes to its run's lane; a standalone
ticket's verdict is recorded on the ticket itself. Standalone testers also
receive the latest existing gate result for the exact candidate head; stale-head
results are excluded.

The candidate diff handed to the tester starts at the merge-base of the head
with the live base branch (`claim.base_branch`), so siblings merged since the
pin are not shown as this candidate's work. The verdict's `Base SHA` stays the
pinned `branch_base_sha`; it is also the fallback when the branch is not
resolvable. Integration itself is a git merge (`integrate.go`) and builds no diff.
