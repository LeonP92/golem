# internal/wiki

This module implements a TF-IDF vector search index over wiki documents (markdown files). It exists to provide semantic-quality document retrieval without external API dependencies or credentials — pure Go cosine similarity search that detects near-duplicate code and related modules. The module handles the full lifecycle: loading markdown files from disk with SHA-256 content hashing, building and normalizing TF-IDF vectors, extracting co-mention link edges between documents, searching with optional one-hop link expansion, and persisting/restoring the index to disk via gob encoding. Staleness detection ensures the index is rebuilt whenever wiki content changes.

## Functions

- func Build(docs []Doc) *Index — builds a TF-IDF index with co-mention link graph from a slice of documents
- func (idx *Index) Search(query string, topK int) []Match — returns topK documents ranked by cosine similarity to query
- func (idx *Index) SearchExpanded(query string, topK int) []Match — returns TF-IDF matches plus one-hop linked documents with half-score provenance
- func SaveToFile(idx *Index, path string) error — serializes the index to disk via gob encoding
- func LoadFromFile(path string) (*Index, error) — deserializes a previously saved index from disk
- func EnsureIndex(wikiDir, indexPath string) (*Index, error) — loads a fresh on-disk index or rebuilds it if missing or stale
- func LoadAll(wikiDir string) ([]Doc, error) — recursively reads all .md files under wikiDir into Doc slices
- func Hash(content string) string — returns a SHA-256 hex digest of a string
- func IsStale(doc Doc, storedHash string) bool — reports whether a doc's content has changed from a stored hash

## Types

- Doc — a wiki document with file path, text content, and SHA-256 content hash
- Index — TF-IDF search index holding document vectors, vocabulary, and co-mention link graph
- Match — a search result with document path, similarity score, and optional Via provenance path
