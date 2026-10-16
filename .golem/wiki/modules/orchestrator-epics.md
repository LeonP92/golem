# orchestrator-epics

`internal/orchestrator/epics` — the stateful half of an Epic Run. The pure
plan core (schema, validation, hash, readiness order) is `internal/epic`.

States: `draft` → `awaiting-approval` → `running` ⇄ `paused` →
`awaiting-final-review` → `completed`. `needs-attention` and `cancelled`
are reserved (accepted by the gates, set by nothing yet).

## Planning (epics.go)
- `Create` validates branch strategy (`epic-branch` | `trunk-flags`) and
  delivery target (`feature-branch` | `base-branch`; trunk-flags cannot use
  feature-branch). Epic-branch runs integrate into `epic/<epic id>`.
  Auto-merging children or the final PR forces `TesterRequired`.
- `AddRevision` validates and stores an immutable revision (plus node/edge
  snapshots) and clears any approval. Only before the run starts.
- `Approve` binds the actor to the exact revision AND plan hash, and only
  for the current revision of a run still awaiting approval
  (`ErrNotCurrent` otherwise).

## Running (start.go, pause.go)
- `Start` materializes the approved revision into ordinary tickets, once:
  ID `epic-<id8>-<lowercased key>`, `BaseBranch` = node branch target,
  description composed verbatim from the plan, and
  `ManualApproval = !AutoApprovePhases` snapshotted per child (later policy
  changes never reach existing children).
- Readiness (`EpicReady`) is derived from the plan plus receipts at start
  and on every receipt. Claims re-check it in SQL (see orchestrator-api).
- `RecordIntegration` writes the receipt readiness consumes (merge SHA,
  ref, time). Identical repeats are one receipt; a different SHA fails
  closed. Accepted while running, paused or needs-attention. The last
  receipt moves the run to `awaiting-final-review`, or to `completed` under
  trunk-flags (it already integrated into base).
- `Pause`/`Resume`: running ⇄ paused. Every claim path requires `running`,
  the lane no-ops otherwise, and the PR monitor leaves such children alone.
  A phase already executing finishes. Resume refuses `needs-attention`.

## Integration lane (integrate.go)
`AttemptMerge` is called by the PR monitor for a child whose PR has no
failed checks. It merges only when the epic auto-merges children, the run
is `running`, GitHub reports the PR mergeable and `clean`, the PR targets
the plan's ref, and (when required) the newest tester verdict for this
exact head is `pass`. Under a per repo+ref lease it reconciles stale
`merging` rows from the remote, writes the attempt row before the merge
call, checks its fencing token, and merges pinned to the head. Outcomes:
`merged` (then a receipt), `failed-stale` (head moved), `failed-forbidden`
(403), `failed-unknown` (retried on the same head), `failed-conflict`,
`blocked-tester`. One attempt row per (epic, node, head).

## Tester verdicts (verdict.go)
`RecordVerdict` stores a verdict (pass/fail/inconclusive + head SHA) and
deletes the `blocked-tester` row for that head so the lane re-evaluates.
Verdicts are append-only and matched on head SHA; a verdict for another
head is ignored.

## Final merge (finalmerge.go)
`MergeFinal` (`POST /api/epics/{id}/merge-final`, run-view button) merges
the `epic/<id>` → base PR for a base-branch run with `AutoMergeFinal`, in
`awaiting-final-review`, given a current final-candidate pass (empty node
key). Pinned to the head; `failed-unknown` retries, other terminal
attempts are reported as errors; an already-merged PR converges to
`completed`.

Gap: nothing opens the final PR or produces a final-candidate verdict
(shems may only post verdicts for node tickets they hold), so final
auto-merge cannot complete on its own yet.
