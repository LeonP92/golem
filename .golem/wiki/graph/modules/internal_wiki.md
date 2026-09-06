# internal/wiki

This module implements a TF-IDF vector search index over wiki documents (markdown files), providing semantic-quality document retrieval without external API dependencies or credentials. It handles the full lifecycle: loading markdown files from disk with SHA-256 content hashing via LoadAll and Hash, building and normalizing TF-IDF vectors with co-mention link edge extraction, searching with optional one-hop link expansion via SearchExpanded, and persisting/restoring the index to disk via gob encoding. Staleness detection in EnsureIndex ensures the index is rebuilt whenever wiki content changes, making it the single entry point for callers that need an always-fresh index.

## Functions

- Build
- Search
- SearchExpanded
- SaveToFile
- LoadFromFile
- EnsureIndex
- TestBuildLinksDetectsCoMentions
- TestBuildLinksIgnoresSelfReference
- TestSearchExpandedIncludesLinkedDocs
- TestSearchExpandedNoDuplicates
- TestSearchRanksSimilarDocAboveUnrelatedDoc
- TestSearchFindsNearDuplicateWithDifferentWording
- TestSearchReturnsEmptyForEmptyIndex
- TestSaveAndLoadRoundTrip
- TestLoadFromFileMissingReturnsError
- TestEnsureIndexBuildsWhenMissing
- TestEnsureIndexRebuildsWhenWikiContentChanges
- TestEnsureIndexReusesFreshIndex
- LoadAll
- Hash
- IsStale
- TestLoadAllReadsMarkdownFiles
- TestHashIsStableAndDistinguishesContent
- TestIsStaleDetectsChangedContent

## Types

- Match
- Index
- Doc

## Imports

encoding/gob, math, os, path/filepath, regexp, sort, strings, testing, crypto/sha256, encoding/hex
