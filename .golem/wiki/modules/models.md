# models

`internal/models` — agent stages, model catalogs, and per-stage model
resolution. Stdlib only, no catalog data as a runtime source of truth.

## Types

- `Stage` and `Stages` — the closed set of agent invocation sites, in
  display order.
- `Tier` — an operator-defined label. `Catalog.Tiers` lists them cheapest
  first.
- `Model` — `{ID, Label, Tier}`.
- `Catalog` — `{SupportsSelection, Tiers, Models, StageDefaults}`, a backend's
  selectable models and the tier each stage falls back to.
- `Selections` — stage name (or `DefaultKey`) → model id or `tier:<label>`.

## Functions

- `TierRef` splits a `tier:<label>` value; `TierValue` builds one.
- `ValidModelID` reports whether an id is safe as a single argv token; `/` is
  allowed for provider-qualified ids (`ollama/kimi-k2.7-code`).
- `Catalog.Validate` reports the first structural fault in a catalog.
- `Catalog.Has` and `Catalog.ValidateSelections`.
- `Catalog.ForTier` returns the first model at a tier, else the nearest
  cheaper, else the nearest larger, else `""`.
- `Resolve` returns a model id for every stage plus the selections it dropped.
  A backend that does not support selection resolves every stage to `""`.
- `Union` merges the catalogs several shems report for one backend name.
- `ValidateSelections` (package level) checks a raw submission against a set of
  named catalogs and returns what to store plus the backend to bind it to. A
  tier-only selection binds no backend.
- `Describe` renders a selection as `default=opus, plan=sonnet`.
- `FieldPrefix`, `BackendField` and `FromValues` are the one decoder for the
  `model_backend` / `model_<stage>` wire format, shared by the API form reader,
  the UI form reader and the htmx query reader.

`diagnose` is the failure diagnosis invocation site. It uses the same
backend catalog, stage-default resolution and ticket selection as every stage.
Embedded Claude Code, Codex and OpenCode catalogs default it to medium.

`epic-plan` is the stage for epic plan-generation sessions (shem `runPlanJob`).
It defaults to the large tier because the plan shapes every downstream ticket.
