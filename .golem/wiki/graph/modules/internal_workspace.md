# internal/workspace

The workspace module manages Git worktrees for Golem tickets. It provides two operations: creating an isolated worktree and branch for a given ticket ID at a deterministic path under .golem/tickets/, and removing that worktree and branch when the ticket is done. The module shells out to git directly and is tolerant of already-absent worktrees or branches during removal.

## Functions

- func Create(repoRoot, ticketID, branchBaseSHA string) (worktreePath, branch string, err error) — creates a git worktree and branch for a ticket under .golem/tickets/<ticketID>/worktree
- func Remove(repoRoot, worktreePath, branch string) error — removes the git worktree directory and deletes the branch, tolerating already-absent state
