# orchestrator-ghsync (epic additions)

- Epic children have no issue, yet `enqueuePRIfReady` queues their PR (no
  `Closes #`) and the drain opens it non-draft so it can be merged.
- Merged child PR into the child's base: `epics.RecordIntegration` with the
  merge commit, then the ticket closes. A retryable receipt failure leaves
  the ticket open; a conflict, a finished run, or a merge into another
  base is logged and the ticket closes.
- Children of a run that is not `running` are neither fixed nor merged.
- Clean PRs go to `epics.MergeChild`; its fix/park result goes through
  `needsFixing` / `setPhase(needs-attention)`.

`github.MergePullRequest(ctx, owner, repo, number, expectedHeadSHA)` merges
pinned to the head: 409 → `ErrMergeConflictHead`, 403 →
`ErrMergeForbidden`, anything else is a plain error.
