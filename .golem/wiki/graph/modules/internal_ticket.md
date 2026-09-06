# internal/ticket

The ticket module defines the lifecycle state for a Golem development ticket. It models the discrete phases a ticket moves through (brainstorm, plan, approve, implement, review, ready-for-review, needs-attention, close, closed), holds associated metadata such as branch name and worktree path, and provides JSON-backed persistence via Save and Load. New tickets start at PhaseBrainstorm unless marked trivial, in which case they skip directly to PhasePlan.

## Functions

- New
- Save
- Load
- TestNewSetsDefaults
- TestNewTrivialSkipsBrainstorm
- TestSaveAndLoadPreservesExpectedLines
- TestSaveAndLoadRoundTrip

## Types

- Phase
- State

## Imports

encoding/json, os, path/filepath, testing
