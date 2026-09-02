# internal/bloat

The bloat package provides a deterministic scope-bloat check for commit diffs. It compares the actual number of changed lines against a plan step's stated expectation and flags the diff as SCOPE_BLOAT when the actual count exceeds a fixed multiplier threshold. When no expected line count is provided, the check abstains, deferring scope judgment to LLM roles.

## Functions

- func Check(actualLines, expectedLines int) (exceeded bool, ratio float64) — compares actual diff line count against plan expectation, returning whether scope bloat threshold is exceeded and the computed ratio
