# epic

`internal/epic` — the deterministic core of an Epic Run: plan schema,
validation, content identity and readiness. No I/O, no model calls.
Persistence and execution: see orchestrator-epics.

- `Plan` is one immutable revision: requirements, nodes (proposed tickets,
  in plan order) and hard edges. Soft file-overlap hints are not edges.
- `Validate` reports every problem in one pass: well-formed IDs, node keys
  unique ignoring case (ticket IDs lower-case them), no self-edges or
  unknown endpoints, acyclic, requirement coverage in both directions,
  non-empty acceptance commands and branch targets, at most `MaxNodes`.
- `Hash` is SHA-256 over canonical JSON; approvals bind to it.
- `Ready(plan, integrated)` marks nodes not yet integrated whose
  predecessors all are (merged with a receipt).

Acceptance commands are carried into the child's description and run by
its gate (`ticket new --accept`).
