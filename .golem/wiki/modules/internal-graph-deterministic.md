# internal/graph — deterministic pipeline

Module pages in `.golem/wiki/graph/modules/` are rendered from tree-sitter
extraction (package docs, function signatures, type fields, exported
consts, imports), with no per-module LLM call. The LLM runs once per
subsystem cluster to write the narrative under each `## <subsystem>`
heading in `graph/index.md`.

Pipeline (`internal/cli/graphbuild.go`, `graphupdate.go`):
1. `graph.Discover` groups source files by directory into modules.
2. `buildModuleGraphs` extracts every module in parallel
   (`--concurrency`) via `graph.Extract(src, lang)`, which dispatches to
   per-language extractors (Go, Python, JS/TS/TSX, Java, Rust, Ruby).
   Other discovered languages get a page with no symbols.
3. `graph.CoarsenSubsystems` tags each module with its first meaningful
   path segment (scaffolding such as `internal`, `cmd`, `pkg`, `src`,
   `lib`, `staging`, `vendor`, `packages` and dotted host segments are
   skipped), then descends one segment at a time for any cluster over
   `DefaultCoarsenClusterSize` (200) modules — tags can be multi-segment,
   e.g. `client-go/dynamic`.
4. `runSubsystemNarratives` calls the `graph-builder` role once per
   cluster, in parallel. Results are cached in
   `.golem/index/subsystem-narratives.json` keyed by `graph.ClusterHash`
   (subsystem + module paths + package docs), so body-only edits never
   reach the LLM. `graph.ValidateNarrative` gates output (≥100 chars,
   names ≥2 cluster modules by leaf segment, no template phrases);
   rejections render a visible `graph.StubNarrative` and are cached as
   failures — `graph update` keeps them, `graph build` retries them.
   Runner errors are not cached. Superseded hashes are pruned.

`graph update` re-extracts only directories touched by `git diff
<base>..HEAD` (including deletions; a directory left with no source
files is removed), re-coarsens against the full stored graph, and
re-renders only rebuilt modules plus any whose subsystem tag moved.
`graph build` starts from a clean module store and meta.

`graph.ModuleGraph` (`module.go`) is the persisted record. `Summary` is
read only from pre-extraction indexes; `ExportFns`/`ExportTypes` mirror
the names in `ExportedFuncs`/`ExportedTypes` for name-only readers
(`graphdeps`, `whoimports`, `symbols.md`).

A repo whose `.golem/roles/graph-builder.md` is still the old per-module
prompt (contains `GRAPH_SUMMARY`) gets the embedded narrative role
instead, with a warning.
