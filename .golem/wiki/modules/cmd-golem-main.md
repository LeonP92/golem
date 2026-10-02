# cmd/golem/main.go

Entry point and command dispatcher for the golem CLI. Routes top-level commands to their handler functions via a `commandTable` map. Added `golem help` / `golem help <command>` in the graph-subsystem ticket: iterates `commandGroups` (ordered groups by area) to print one-line descriptions, or delegates to the target command's `--help` flag output for per-command usage.

`golem models` (`modelsDispatch`) is the model command group: `list` prints a
backend's tiers, models and per-stage default models; `probe` checks each
catalog model against the vendor CLI and prints the verified `backend:` block.
Both read a `shem.yaml` via `--config` or a standalone document via
`--backend-config`.
