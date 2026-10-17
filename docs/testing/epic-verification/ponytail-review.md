What this change does: The epic branch coordinates approved child tickets through
existing workers and the PR monitor. Recovery fixes supply missing review/tester
evidence, preserve acceptance gates, reject old workers and serialize receipts.
The expected load is one orchestrator per database with multiple shems and
simultaneous child completions.

Should fix:

1. **Human pushes need a manual retest** (`internal/orchestrator/epics/integrate.go`).
   - What this is: MergeChild requires a tester verdict for the current PR head.
   - Problem: A human push changes that head, but no worker is scheduled to test
     it. The merge remains blocked even when the new code is correct.
   - Fix: Add an explicit retest path that fetches and tests the remote head,
     reusing the worker and exact-head verdict contract. Until then, request a
     revision and incorporate the remote changes explicitly.
   - If we skip it: Safe automatic merging stops until a person intervenes.

The review found no additional concrete bloat to remove in the recovery changes.
They reuse ticket.State, the escaped prompt builder, existing ownership queries,
the PR monitor and the existing environment regression test. No dependency,
second orchestration state machine or background final merge was added.

The missing-plan observer, missing-source tester, state-loss, old-worker claim,
receipt race and backend credential findings were fixed before this final pass.
Golem's convention and spec reviews reported no remaining findings; its reviewer
returned VERDICT: PASS. Full race tests, PostgreSQL tests, vet, build and pinned
lint passed.

Verdict: The reviewed fixes are ready. Document the manual human-push retest
requirement; do not claim complete Claude E2E while its quota is exhausted.
Not checked: a final-worker deployment/restart soak test, external real-GitHub
branch protection, and Claude completion after the external quota resets.
