package wiki

import (
	"encoding/gob"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Match struct {
	Path  string
	Score float64
	Via   string // non-empty when this result was reached by link expansion, not direct TF-IDF
}

// Index is a TF-IDF vector search index over wiki documents. This is a
// deliberate v1 simplification of the spec's "embeddings index": the
// spec named semantic search as the goal but did not specify an
// embedding provider, and adding one would mean either an external API
// dependency (contradicting "Golem stores no credentials") or an
// arbitrary vendor choice. TF-IDF cosine similarity gets meaningfully
// better-than-grep retrieval with zero network calls and zero
// credentials — pure Go, no cgo.
type Index struct {
	Docs    []Doc
	Vocab   map[string]int   // term -> index into each vector
	Vectors [][]float64      // one vector per Doc, same order as Docs
	Links   map[string][]string // extracted co-mention edges: path -> paths mentioned by that doc
}

var tokenRe = regexp.MustCompile(`[a-zA-Z0-9]+`)

func tokenize(text string) []string {
	return tokenRe.FindAllString(strings.ToLower(text), -1)
}

func Build(docs []Doc) *Index {
	vocab := make(map[string]int)
	docTokens := make([][]string, len(docs))
	for i, d := range docs {
		tokens := tokenize(d.Content)
		docTokens[i] = tokens
		for _, tok := range tokens {
			if _, ok := vocab[tok]; !ok {
				vocab[tok] = len(vocab)
			}
		}
	}

	df := make([]int, len(vocab)) // document frequency per term
	for _, tokens := range docTokens {
		seen := make(map[string]bool)
		for _, tok := range tokens {
			if !seen[tok] {
				df[vocab[tok]]++
				seen[tok] = true
			}
		}
	}

	n := float64(len(docs))
	vectors := make([][]float64, len(docs))
	for i, tokens := range docTokens {
		tf := make(map[string]int)
		for _, tok := range tokens {
			tf[tok]++
		}
		vec := make([]float64, len(vocab))
		for tok, count := range tf {
			idx := vocab[tok]
			idf := math.Log((n + 1) / (float64(df[idx]) + 1))
			vec[idx] = float64(count) * idf
		}
		vectors[i] = normalize(vec)
	}

	return &Index{Docs: docs, Vocab: vocab, Vectors: vectors, Links: buildLinks(docs)}
}

// buildLinks scans each doc's content for mentions of other docs by
// their single-token file stem (e.g. "auth" from "auth.md"). Multi-token
// stems (e.g. "validate_us_phone") are skipped — they're too ambiguous
// to match reliably against prose. Only single-word stems are extracted
// so the matching is the same deterministic token scan the TF-IDF index
// uses, with no false positives from substring matching.
func buildLinks(docs []Doc) map[string][]string {
	stems := make(map[string]string, len(docs))
	for _, d := range docs {
		base := strings.TrimSuffix(filepath.Base(d.Path), ".md")
		toks := tokenize(base)
		if len(toks) == 1 {
			stems[toks[0]] = d.Path
		}
	}

	links := make(map[string][]string)
	for _, d := range docs {
		seen := make(map[string]bool)
		for _, tok := range tokenize(d.Content) {
			if target, ok := stems[tok]; ok && target != d.Path && !seen[target] {
				links[d.Path] = append(links[d.Path], target)
				seen[target] = true
			}
		}
	}
	return links
}

func normalize(v []float64) []float64 {
	var sumSq float64
	for _, x := range v {
		sumSq += x * x
	}
	if sumSq == 0 {
		return v
	}
	norm := math.Sqrt(sumSq)
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}

func (idx *Index) vectorize(query string) []float64 {
	vec := make([]float64, len(idx.Vocab))
	for _, tok := range tokenize(query) {
		if i, ok := idx.Vocab[tok]; ok {
			vec[i] += 1
		}
	}
	return normalize(vec)
}

func cosine(a, b []float64) float64 {
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	return dot
}

// Search returns the topK documents most similar to query, ranked
// highest score first.
func (idx *Index) Search(query string, topK int) []Match {
	if len(idx.Docs) == 0 {
		return nil
	}
	q := idx.vectorize(query)
	matches := make([]Match, len(idx.Docs))
	for i, d := range idx.Docs {
		matches[i] = Match{Path: d.Path, Score: cosine(q, idx.Vectors[i])}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Score > matches[j].Score })
	if topK < len(matches) {
		matches = matches[:topK]
	}
	return matches
}

// SearchExpanded returns the topK TF-IDF matches, then appends any docs
// that those matches link to (one hop only) and are not already in the
// direct results. Linked results score at half the referring doc's score
// so they always sort after direct matches. The Via field on each linked
// Match names the doc that mentioned it, so callers can show provenance.
func (idx *Index) SearchExpanded(query string, topK int) []Match {
	direct := idx.Search(query, topK)
	if len(idx.Links) == 0 {
		return direct
	}
	seen := make(map[string]bool, len(direct))
	for _, m := range direct {
		seen[m.Path] = true
	}
	var linked []Match
	for _, m := range direct {
		for _, linkedPath := range idx.Links[m.Path] {
			if !seen[linkedPath] {
				seen[linkedPath] = true
				linked = append(linked, Match{Path: linkedPath, Score: m.Score * 0.5, Via: m.Path})
			}
		}
	}
	return append(direct, linked...)
}

func SaveToFile(idx *Index, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return gob.NewEncoder(f).Encode(idx)
}

func LoadFromFile(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var idx Index
	if err := gob.NewDecoder(f).Decode(&idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// EnsureIndex is the one place staleness, rebuild, and search come
// together: it loads the on-disk index if it's still fresh, or rebuilds
// it from current wiki content if it's missing or any document's hash
// has changed since the index was last saved (spec: Error Handling).
func EnsureIndex(wikiDir, indexPath string) (*Index, error) {
	docs, err := LoadAll(wikiDir)
	if err != nil {
		return nil, err
	}

	if existing, err := LoadFromFile(indexPath); err == nil && !needsRebuild(docs, existing) {
		return existing, nil
	}

	idx := Build(docs)
	if err := SaveToFile(idx, indexPath); err != nil {
		return nil, err
	}
	return idx, nil
}

func needsRebuild(current []Doc, idx *Index) bool {
	if len(current) != len(idx.Docs) {
		return true
	}
	stored := make(map[string]string, len(idx.Docs))
	for _, d := range idx.Docs {
		stored[d.Path] = d.Hash
	}
	for _, d := range current {
		if stored[d.Path] != d.Hash {
			return true
		}
	}
	return false
}
