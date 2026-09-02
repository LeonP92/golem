# internal/observer

The observer module implements the single persistent, deterministic process that mediates between incoming commit signals and one-shot agent invocations. Its core responsibility is preventing duplicate work: it claims each (role, commitSHA) pair in-memory to guard against concurrent goroutines racing within a process, and cross-checks the blog log to skip signals already handled by a prior Observer lifetime. When a signal is genuinely new, it invokes an agent runner, classifies the output as FINDING, BLOCKER, or silence, and appends the result to the structured log.

## Functions

- New(logPath string, runner agentrunner.Runner) *Observer — constructs an Observer with an empty in-memory claim map
- (o *Observer) IsInFlight(role, commitSHA string) bool — reports whether a dispatch for this role+commit is already claimed in the current process lifetime
- (o *Observer) DispatchForCommit(role, commitSHA, diff, rolePrompt string) error — claims a signal, checks for prior-run log entries, runs the agent, classifies output, and appends FINDING/BLOCKER to the blog

## Types

- Observer — persistent per-ticket coordinator that claims signals and dispatches one-shot agent invocations with duplicate-prevention guarantees

## Imports

github.com/leonpham/golem/internal/agentrunner, github.com/leonpham/golem/internal/blog
