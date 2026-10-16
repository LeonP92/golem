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
- `Ready(plan, State)` returns nodes whose predecessors are all integrated
  (merged with a receipt), ordered by critical-path depth, then plan order.

Acceptance commands are carried into the child's description; they are
not run by the gate (only `gate.commands` are).
