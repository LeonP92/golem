# orchestrator-epics

`internal/orchestrator/epics` — the stateful half of an Epic Run. The pure
plan core (schema, validation, readiness) is `internal/epic`. Children are
ordinary tickets; the run adds two tables (`epics`, `epic_plan_revisions`),
the append-only `tester_verdicts` (scoped to an epic node or, for the
ticket-level tester stage, to a standalone ticket), and epic columns on
`tickets`.

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
- `RecordIntegration` writes a child's receipt (`EpicIntegratedSHA`),
  recomputes every child's `EpicReady`, and on the last receipt moves the
  run to `awaiting-final-review` (`completed` under trunk-flags). See the
  lane section below for the full receipt semantics.
- `Pause`/`Resume`: running ⇄ paused. Claims and the lane need `running`.

## Integration lane (gate.go)
The merge lives on the shem that did the child's work (see
shem-worker and forge); the orchestrator side is a gate plus a receipt.
`GateIntegration` checks every precondition (run `running` and
auto-merging, node in the plan, child `ready-for-review`, a tester pass
for this exact head when required) and grants the per-run integration
slot: one row per epic (`epic_integration_slots`, PK = epic, 5-minute
TTL), taken by INSERT on an empty lane or by conditional UPDATE when the
holder lapsed or is re-checking its own node; a live holder answers
`wait` with its expiry as the retry hint. The slot is bound to
(node, head SHA), so a takeover never merges a stale head under a fresh
one's lease. Outcomes: `granted` (with `TargetRef` and the expiry),
`wait`, `done` (receipt already recorded — crash retry).

`RecordIntegration` (start.go) writes the receipt: it frees the slot in
the same transaction, is idempotent in any run state (an identical repeat
is a no-op even after the run moved on, a different SHA is
`ErrIntegrationConflict`), unblocks successors, and moves a fully
integrated run on — carrying variadic emit hooks fired on the transition
to `awaiting-final-review`, which is where the GitHub add-on hangs its
final pull request (`ghsync.EnqueueFinalPR`). The core passes no hooks
and behaves identically. `ReleaseIntegration` frees the slot when the
lane ends without a receipt (conflict, moved head, error).

## Final merge (complete.go)
Always a human act. `CompleteFinal` (`POST /api/epics/{id}/merge-final`,
or the dashboard's mark-completed form) is an attestation, not an action:
the caller states the integration branch is merged to base and names the
resulting head SHA, and the run closes on that word. Repeating the same
attestation is a no-op; `AutoMergeFinal` is gone.
