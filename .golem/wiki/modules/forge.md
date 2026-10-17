# forge

`internal/forge` — merges a finished branch into a target ref on any git
remote. The shem-side half of epic integration: plain git commands in a
Golem-owned bare mirror, no forge vendor involved. `Forge` is an
interface (`HeadSHA`, `MergeIntoTarget`) so the worker depends on the
operation, not the git CLI; `Git` is the only implementation by design —
plain git speaks to every remote already, and a local path works, which
is what no_push and `local://` runs merge through.

## MergeIntoTarget

One fetch-merge-push pass inside the mirror, retried only on a lost
compare-and-swap (`MaxAttempts`, default 4), serialized per mirror path:

- Fetch the branch and the target ref separately (one fetch naming both
  would fail when the run's first child merges before the target exists);
  stale scratch refs in the mirror are dropped first.
- Pinned head: a remote head that no longer equals `Merge.HeadSHA` is
  `Moved` — the caller re-pins at the gate and re-runs; nothing merges.
- A missing target is created at the pinned head (the first child's
  branch IS base plus its commits); create races resolve through the same
  CAS retry as a moved target.
- An already-contained head is `Contained` (crash retry — only the
  receipt is owed). Otherwise a `--no-ff` merge runs in a temporary
  worktree (crash debris is force-removed first) and the result is pushed
  with an explicit `--force-with-lease` expectation for the target SHA
  (empty for creation). A stale target is retried; a permission refusal
  is `Rejected` with the remote's
  own words, and a dirty merge is `Conflict` with the conflicted file
  list. Exhausting the CAS retry budget is an operational error, so
  polling retries contention without parking the child.
- Operational failures (git binary, network, mirror) come back as Go
  errors, never as statuses — they are retried later, not reported as
  verdicts.

Local paths and `file://` transports never execute agent-owned receive or
pack-object hooks. They use a filtered environment and explicit trusted Git
helpers; network transports retain the shem's credential helper. Parsed Git
results use stdout only, keeping warnings out of head SHAs.
