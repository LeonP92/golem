# internal/graph — deterministic pipeline

Per-module pages in `.golem/wiki/graph/modules/` are now rendered from
verbatim tree-sitter extraction (package docs, function signatures, type
fields, exported consts, imports) with no LLM call in the hot path. The
LLM is invoked once per subsystem cluster for a cross-cutting narrative
that renders under each `## <subsystem>` header in `graph/index.md`.

Key entry points:
- `graph.Extract(source, langName) (*StructuralData, error)` dispatches
  to per-language extractors: `extractGo`, `extractPython`,
  `extractJSTS`, `extractJava`, `extractRust`, `extractRuby`. Each walks
  the tree-sitter AST directly to associate leading comments with their
  declaration (line-adjacent only). Unknown languages fall back to a
  generic `.scm`-query path that produces name-only exports.
- `graph.ModuleGraph` (`internal/graph/module.go`) is the persisted
  per-module record. Rich fields `ExportedFuncs` / `ExportedTypes` /
  `Consts` / `PackageDoc` sit alongside legacy `ExportFns` /
  `ExportTypes` `[]string` name slices, which are auto-populated for
  backward compat with `graphdeps` / `graphwhoimports` /
  `graphcheckboundary`.
- `graph.SubsystemForPath(modulePath)` derives a subsystem tag from the
  first non-scaffolding path segment (skips
  `internal|cmd|pkg|src|lib`), returns `"misc"` for empty/all-skip
  paths.
- `graph.WriteModule` / `graph.WriteIndex` render markdown pages
  deterministically. `WriteIndex` takes an optional
  `SubsystemNarratives` map that renders under each subsystem header.

Subsystem-narrative pass (`internal/cli/graphbuild.go` →
`runSubsystemNarratives`):
- Clusters modules via `graph.ClusterBySubsystem`.
- Hashes each cluster (`graph.ClusterHash`) over subsystem + module
  paths + `PackageDoc` excerpts and caches results in
  `.golem/index/subsystem-narratives.json`. Unchanged clusters skip the
  LLM entirely (idempotent).
- Prompt body from `graph.BuildClusterPrompt` (subsystem name, module
  list, top-3 `PackageDoc` excerpts). Role prompt from
  `graph-builder.md`, which is now deprecated for per-module use but
  retained for this pass.
- `graph.ExtractNarrativeJSON` unwraps the JSON envelope;
  `graph.ValidateNarrative` gates on min 100 chars, ≥2 leaf-module
  references, and forbidden template phrases. Failed validation renders
  a visible `graph.StubNarrative` line into `index.md` — never a silent
  skip.

Removed: `internal/graph/parser.go` (LLM-output tag scraper) and the
per-module `agentrunner.RunAgent("graph-builder", …)` call from
`graphbuild.go` / `graphupdate.go`.
