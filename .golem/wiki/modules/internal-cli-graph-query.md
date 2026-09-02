# internal/cli — graph query commands

Added in the graph-query ticket: two new CLI commands for interrogating the module graph index.

## GraphWhoImports

`func GraphWhoImports(args []string, stdout, stderr io.Writer) int`

Accepts a single positional `<module>` argument plus `--repo`. Loads the full graph index via `graph.LoadAllModuleGraphs` and prints every module whose `Imports` list contains the target (case-insensitive). If nothing imports the target it prints a "no modules import" message. Exits 0 in all non-error cases.

## GraphCheckBoundary

`func GraphCheckBoundary(args []string, stdout, stderr io.Writer) int`

Accepts `--subsystem <name>` (required) and `--repo`. Loads the graph index, collects all modules belonging to the named subsystem, then scans every other module for imports that cross into that subsystem. Prints a `VIOLATION  <caller>  →  <callee>` line for each violation, then a summary count. If no violations are found prints a clean message. Exits 0 in all non-error cases.

## Registration

Both commands are wired into `graphDispatch` in `cmd/golem/main.go` under the subcommand names `who-imports` and `check-boundary`.
