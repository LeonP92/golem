# internal/graph

The graph package implements codebase indexing by discovering source files, parsing structured LLM-generated module descriptions, persisting metadata and edges, and writing human-readable wiki documents. It exists to build and maintain a navigable knowledge graph of the repository so that tools and agents can query module relationships, exports, and summaries without re-reading source files from scratch. The package combines tree-sitter-based structural extraction (imports, exported functions and types) across Go, Python, TypeScript, JavaScript, Rust, and Java with an LLM cache backed by atomic JSON persistence to avoid redundant API calls when file contents are unchanged.

## Functions

- TestAtomicWriteFile_writesContent
- TestAtomicWriteFile_overwritesExisting
- TestAtomicWriteFile_leavesNoTempOnSuccess
- TestAtomicWriteFile_setsPermissions
- LoadLLMCache
- Get
- Set
- Save
- ModuleCacheKey
- TestModuleCacheKey_deterministic
- TestModuleCacheKey_changesWithContent
- TestLLMCache_roundTrip
- TestLLMCache_missingFileReturnsEmpty
- TestLLMCache_concurrentGetSet
- Discover
- TestExtSet_includesDefaults
- TestExtSet_includesExtras
- TestSkipped
- LangForExt
- Extract
- TestLangForExt
- TestExtract_go
- TestExtract_python
- TestExtract_javascript
- TestExtract_unknownLang
- LoadMeta
- SaveMeta
- SaveEdges
- SaveModuleGraph
- LoadAllModuleGraphs
- FileHash
- ModuleHashes
- DeleteModuleGraph
- Parse
- TestParse_happyPath
- TestParse_missingTags
- TestParse_ignoresStructuralTags
- TestParse_moduleStillParsed
- WriteModule
- AppendSymbols
- AppendTypes
- WriteIndex
- ResetAggregates
- Slug
- TestSlug
- TestWriteModule_idempotent
- TestWriteIndex_groupsBySubsystem

## Types

- LLMCache
- Module
- StructuralData
- ModuleMeta
- Meta
- Edges
- ModuleGraph

## Imports

os, path/filepath, testing, crypto/sha256, encoding/json, errors, fmt, sort, strings, sync, os/exec, embed, github.com/tree-sitter/go-tree-sitter, github.com/tree-sitter/tree-sitter-go/bindings/go, github.com/tree-sitter/tree-sitter-java/bindings/go, github.com/tree-sitter/tree-sitter-javascript/bindings/go, github.com/tree-sitter/tree-sitter-python/bindings/go, github.com/tree-sitter/tree-sitter-rust/bindings/go, github.com/tree-sitter/tree-sitter-typescript/bindings/go, io
