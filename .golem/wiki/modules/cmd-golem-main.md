# cmd/golem/main.go

Entry point and command dispatcher for the golem CLI. Routes top-level commands to their handler functions via a `commandTable` map. Added `golem help` / `golem help <command>` in the graph-subsystem ticket: iterates `commandGroups` (ordered groups by area) to print one-line descriptions, or delegates to the target command's `--help` flag output for per-command usage.
