# internal/observer

The observer module implements the single persistent, deterministic process that mediates between incoming commit signals and one-shot agent invocations. Its core responsibility is preventing duplicate work: it claims each (role, commitSHA) pair in-memory to guard against concurrent goroutines racing within a process, and cross-checks the blog log to skip signals already handled by a prior Observer lifetime. When a signal is genuinely new, it invokes an agent runner, classifies the output as FINDING, BLOCKER, or silence, and appends the result to the structured log.

## Functions

- New
- IsInFlight
- DispatchForCommit
- TestDispatchForCommitAppendsFindingFromResult
- TestDispatchForCommitSkipsAlreadyClaimedSignal
- TestDispatchForCommitSkipsIfAlreadyLoggedFromPriorRun

## Types

- Observer

## Imports

strings, sync, github.com/leonp92/golem/internal/agentrunner, github.com/leonp92/golem/internal/blog, path/filepath, testing
