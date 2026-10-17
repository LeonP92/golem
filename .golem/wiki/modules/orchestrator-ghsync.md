# orchestrator-ghsync (epic additions)

The add-on's whole role in runs is the final pull request; children never
touch it. `enqueuePRIfReady` requires an issue number, so epic children
get no pull request (they merge into the run's target ref through the
shem-side lane — no GitHub involved), and the PR monitor skips them.

- `EnqueueFinalPR` is the one hook into the core flow, passed as an emit
  hook to `epics.RecordIntegration` and fired inside the transition to
  `awaiting-final-review`. It is a no-op for trunk-flags runs and for
  remotes with no enabled repository, so the core behaves identically
  with or without the add-on.
- Run-level outbox rows carry `EpicID` (empty for ticket rows); the drain
  routes them to `deliverEpicPR`, which opens the integration branch's
  pull request to base (same at-least-once convergence as the ticket
  path) and records `final_pr_number`/`final_pr_url` on the epic. The
  dashboard links it at awaiting-final-review.

The standalone-ticket paths (draft PR on branch push, monitor, phase
labels, fix loops) are unchanged.
