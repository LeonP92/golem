# internal/graph

The graph package implements codebase indexing by discovering source files, parsing structured LLM-generated module descriptions, persisting metadata and edges, and writing human-readable wiki documents. It exists to build and maintain a navigable knowledge graph of the repository so that tools and agents can query module relationships, exports, and summaries without re-reading source files from scratch.

## Functions

- Discover(repoRoot string, maxFileSizeKB int, extraExts, ignorePatterns []string) ([]Module, error) — discovers source files grouped by directory using git ls-files
- Parse(output string) ModuleGraph — parses tagged LLM output into a ModuleGraph struct
- LoadMeta(indexDir string) (*Meta, error) — loads graph metadata from graph-meta.json
- SaveMeta(indexDir string, m *Meta) error — persists graph metadata to graph-meta.json
- SaveEdges(indexDir string, graphs []ModuleGraph) error — writes import and call edges to graph-edges.json
- SaveModuleGraph(indexDir string, g ModuleGraph) error — persists a single module graph to graph-modules/
- LoadAllModuleGraphs(indexDir string) ([]ModuleGraph, error) — loads all persisted module graphs from graph-modules/
- FileHash(path string) (string, error) — computes SHA-256 hash of a file
- ModuleHashes(repoRoot string, files []string) (map[string]string, error) — computes hashes for a set of files
- WriteModule(wikiDir string, g ModuleGraph) error — writes a module's wiki markdown page
- AppendSymbols(wikiDir string, g ModuleGraph) error — appends exported functions to the aggregate symbols.md
- AppendTypes(wikiDir string, g ModuleGraph) error — appends exported types to the aggregate types.md
- WriteIndex(wikiDir string, graphs []ModuleGraph) error — writes the top-level codebase index grouped by subsystem
- ResetAggregates(wikiDir string) error — clears symbols.md and types.md before a full rebuild

## Types

- Module — a directory path and its list of source file paths
- ModuleGraph — parsed LLM description of a module including exports, imports, calls, and subsystem
- ModuleMeta — per-module commit and file hash snapshot for change detection
- Meta — top-level metadata index mapping module paths to ModuleMeta entries
- Edges — serialized import and call relationships across all modules
