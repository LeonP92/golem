# internal/gate

The gate module executes a configured sequence of shell commands against a working directory, acting as a quality gate that validates a workspace before proceeding. It runs commands sequentially and short-circuits on the first failure, collecting combined output throughout. It exists to enforce pre-merge or pre-commit checks defined in configuration.

## Functions

- func Run(dir string, cfg config.GateConfig) (Result, error) — runs configured gate commands sequentially, stopping at first failure

## Types

- Result — holds the pass/fail outcome and combined stdout+stderr output of gate command execution

## Imports

github.com/leonpham/golem/internal/config
