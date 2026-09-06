# internal/workspace

The workspace module manages Git worktrees for Golem tickets. It provides two operations: creating an isolated worktree and branch for a given ticket ID at a deterministic path under .golem/tickets/, and removing that worktree and branch when the ticket is done. The module shells out to git directly and is tolerant of already-absent worktrees or branches during removal.

## Functions

- Create
- Remove
- TestCreateAndRemoveWorktree

## Imports

fmt, os, os/exec, path/filepath, testing
