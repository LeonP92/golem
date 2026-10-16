# orchestrator-ghsync (epic additions)

- PRs for epic children: children have no issue, yet `enqueuePRIfReady`
  queues their PR (no `Closes #`) and the drain opens it non-draft, so the
  lane can merge it.
- Merged child PR: the monitor writes the node's receipt
  (`epics.RecordIntegration`, merge commit + base ref) and then closes the
  ticket. A retryable receipt failure leaves the ticket open for the next
  tick; a conflicting receipt or a finished run is logged and the ticket
  closes.
- Children of a run that is not `running` are neither dispatched for fixes
  nor merged.
- Clean PRs go to `epics.AttemptMerge` (see orchestrator-epics).
- The outbox drain runs only while holding the `ghsync-outbox-drain`
  lease (see orchestrator-db).

`github.MergePullRequest(ctx, owner, repo, number, expectedHeadSHA)` merges
pinned to the validated head: 409 → `ErrMergeConflictHead`, 403 →
`ErrMergeForbidden`, anything else (including 405, not mergeable yet) is a
plain retryable error. The fake enforces the same pin from `PRStatuses`.
