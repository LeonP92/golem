# workspace

`internal/workspace` — git worktree lifecycle for a ticket.

- `Create(repoRoot, ticketID, branch, branchBaseSHA string) (worktreePath string, err error)`
  — creates `.golem/tickets/<ticketID>/worktree` as a new git worktree on
  `branch` (checked out from `branchBaseSHA`). The branch name is a caller
  responsibility (see `internal/slug.Branch`) rather than derived internally,
  so orchestrator-issued tickets get a human-readable branch name instead of
  `ticket/<id>`.
- `Remove(repoRoot, worktreePath, branch string) error` — removes the
  worktree and deletes the branch (best-effort).
