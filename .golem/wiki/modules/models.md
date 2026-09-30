# models

`internal/models` — agent stages, model catalogs, and per-stage model
resolution. Stdlib only, no catalog data as a runtime source of truth.

## Types

- `Stage` and `Stages` — the closed set of nine agent invocation sites, in
  display order. `IsStage` checks membership.
- `Tier` — an operator-defined label. `Catalog.Tiers` lists them cheapest
  first.
- `Model` — `{ID, Label, Tier}`.
- `Catalog` — `{SupportsSelection, Tiers, Models, StageDefaults}`, a backend's
  selectable models and the tier each stage falls back to.
- `Selections` — stage name (or `DefaultKey`) → model id or `tier:<label>`.

## Functions

- `TierRef` splits a `tier:<label>` value; `TierPrefix` is the marker.
- `ValidModelID` reports whether an id is safe as a single argv token.
- `Catalog.Validate` reports the first structural fault in a catalog.
- `Catalog.Lookup`, `Has`, `DeclaresTier`, `ValidateSelections`.
- `Catalog.ForTier` returns the first model at a tier, else the nearest
  cheaper, else the nearest larger, else `""`.
- `Resolve` returns a model id for every stage plus the selections it dropped.
  A backend that does not support selection resolves every stage to `""`.
- `Union` merges the catalogs several shems report for one backend name.
- `ValidateSelections` (package level) checks a raw submission against a set of
  named catalogs and returns what to store plus the backend to bind it to. A
  tier-only selection binds no backend.
- `Describe` renders a selection as `default=opus, plan=sonnet`.
