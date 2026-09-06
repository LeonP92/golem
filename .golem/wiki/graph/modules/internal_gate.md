# internal/gate

The gate module executes a configured sequence of shell commands against a working directory, acting as a quality gate that validates a workspace before proceeding. It runs commands sequentially and short-circuits on the first failure, collecting combined output throughout. It exists to enforce pre-merge or pre-commit checks defined in configuration.

## Functions

- Run
- TestRunAllCommandsPass
- TestRunStopsAtFirstFailure

## Types

- Result

## Imports

os/exec, strings, github.com/leonp92/golem/internal/config, testing
