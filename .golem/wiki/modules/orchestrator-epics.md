# orchestrator-epics

`internal/orchestrator/epics` — the stateful half of an Epic Run. The pure
plan core (schema, validation, readiness) is `internal/epic`. Children are
ordinary tickets; the run adds two tables (`epics`, `epic_plan_revisions`),
the append-only `tester_verdicts`, and epic columns on `tickets`.

States (`State*` constants): `draft` → `awaiting-approval` → `running` ⇄
`paused` → `awaiting-final-review` → `completed`.

## Planning (epics.go)
- `Create` validates strategy (`epic-branch` | `trunk-flags`) and delivery
  target (`feature-branch` | `base-branch`; trunk-flags needs base-branch).
  Epic-branch runs integrate into `epic/<id>` (`TargetRef`). Any auto-merge
  forces `TesterRequired`.
- `AddRevision` stores an immutable revision and clears any approval; only
  before start. A node's empty `branch_target` becomes `TargetRef`; any
  other value is rejected.
- `Approve` binds the username to the current revision AND its plan hash.
- Errors: `ErrInvalid` (bad input), `ErrNotFound`, and the conflict
  sentinels; `api.writeEpicResult` maps them to 422/404/409.

## Running (start.go)
- `Start` materializes the approved revision into tickets once (ID
  `epic-<id8>-<key>`, `BaseBranch` = `TargetRef`, description from the plan,
  `ManualApproval = !AutoApprovePhases` snapshotted).
- `RecordIntegration` sets the child's `EpicIntegratedSHA` (identical
  repeat = no-op, different SHA = `ErrIntegrationConflict`), recomputes
  every child's `EpicReady`, and on the last receipt moves the run to
  `awaiting-final-review` (`completed` under trunk-flags).
- `Pause`/`Resume`: running ⇄ paused. Claims and the lane need `running`.

## Integration lane (integrate.go)
`MergeChild` is called by the PR monitor for a child PR with no failing
checks. When the run auto-merges children, is `running`, the PR is
`clean` and targets `TargetRef`, and (if required) the newest tester
verdict for this exact head is `pass`, it merges pinned to the head and
writes the receipt. It keeps no state of its own; it returns what the
monitor applies through its existing paths: tester `fail` → `needsFixing`
(back to revising with the evidence, bounded by the PR fix attempts);
`inconclusive`, wrong base or a 403 → needs-attention. No verdict yet or a
moved head → wait for the next pass.

## Final merge (finalmerge.go)
`MergeFinal` (UI button / `POST /api/epics/{id}/merge-final`) merges the
`epic/<id>` → base PR for a base-branch run with `AutoMergeFinal`, given a
pass verdict for the final PR's head (empty node key). Nothing opens the
final PR or produces that verdict yet — by design the final merge is a
human step today.
