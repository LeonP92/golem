# internal/bloat

The bloat package provides a deterministic scope-bloat check for commit diffs. It compares the actual number of changed lines against a plan step's stated expectation and flags the diff as SCOPE_BLOAT when the actual count exceeds a fixed multiplier threshold. When no expected line count is provided, the check abstains, deferring scope judgment to LLM roles.

## Functions

- Check
- TestCheckWithinExpectedDoesNotFlag
- TestCheckWildlyOverFlags
- TestCheckZeroExpectedNeverDivides

## Imports

testing
