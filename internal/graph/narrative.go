package graph

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// SubsystemNarrativeCache persists LLM-generated cluster narratives on
// disk keyed by cluster content hash — running the subsystem pass twice
// against unchanged clusters is a no-op (idempotency requirement,
// landmine 4).
type SubsystemNarrativeCache struct {
	path    string
	entries map[string]narrativeEntry
}

type narrativeEntry struct {
	Subsystem string `json:"subsystem"`
	Narrative string `json:"narrative"`
	Hash      string `json:"hash"`
}

// LoadNarrativeCache reads the cache file at path. A missing file yields
// an empty cache.
func LoadNarrativeCache(path string) (*SubsystemNarrativeCache, error) {
	c := &SubsystemNarrativeCache{path: path, entries: map[string]narrativeEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &c.entries); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *SubsystemNarrativeCache) Get(hash string) (string, bool) {
	e, ok := c.entries[hash]
	if !ok {
		return "", false
	}
	return e.Narrative, true
}

func (c *SubsystemNarrativeCache) Set(subsystem, hash, narrative string) {
	c.entries[hash] = narrativeEntry{Subsystem: subsystem, Hash: hash, Narrative: narrative}
}

func (c *SubsystemNarrativeCache) Save() error {
	data, err := json.MarshalIndent(c.entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(c.path, data, 0o644)
}

// ClusterBySubsystem groups modules by their Subsystem tag; the returned
// map's values are sorted by module path for a deterministic order.
func ClusterBySubsystem(graphs []ModuleGraph) map[string][]ModuleGraph {
	out := map[string][]ModuleGraph{}
	for _, g := range graphs {
		sub := g.Subsystem
		if sub == "" {
			sub = "misc"
		}
		out[sub] = append(out[sub], g)
	}
	for k := range out {
		sort.Slice(out[k], func(i, j int) bool { return out[k][i].Module < out[k][j].Module })
	}
	return out
}

// ClusterHash returns a stable hash for a subsystem cluster covering the
// subsystem name, module paths (sorted), and each module's PackageDoc.
// Any content change invalidates the cached narrative.
func ClusterHash(subsystem string, cluster []ModuleGraph) string {
	var b strings.Builder
	b.WriteString(subsystem)
	b.WriteByte('\n')
	for _, g := range cluster {
		b.WriteString(g.Module)
		b.WriteByte('\t')
		b.WriteString(g.PackageDoc)
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return fmt.Sprintf("%x", sum[:12])
}

// BuildClusterPrompt returns the plain-text data body for an LLM cluster
// narrative call. The role prompt is expected to describe the requested
// output shape (JSON with {subsystem, narrative}).
func BuildClusterPrompt(subsystem string, cluster []ModuleGraph) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Subsystem: %s\n\n", subsystem)
	fmt.Fprintf(&b, "Modules in this subsystem (%d):\n", len(cluster))
	for _, g := range cluster {
		fmt.Fprintf(&b, "- %s\n", g.Module)
	}
	b.WriteString("\nTop-doc excerpts:\n")
	// Pick top 3 modules by PackageDoc length as most-informative.
	sorted := make([]ModuleGraph, len(cluster))
	copy(sorted, cluster)
	sort.SliceStable(sorted, func(i, j int) bool { return len(sorted[i].PackageDoc) > len(sorted[j].PackageDoc) })
	limit := 3
	if len(sorted) < limit {
		limit = len(sorted)
	}
	for i := 0; i < limit; i++ {
		if sorted[i].PackageDoc == "" {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n%s\n", sorted[i].Module, sorted[i].PackageDoc)
	}
	return b.String()
}

// ExtractNarrativeJSON parses an LLM output as a JSON object with a
// "narrative" field. Falls back to returning the raw output when parsing
// fails (models sometimes emit extra prose around the JSON).
func ExtractNarrativeJSON(output string) string {
	start := strings.Index(output, "{")
	end := strings.LastIndex(output, "}")
	if start >= 0 && end > start {
		var v struct {
			Narrative string `json:"narrative"`
		}
		if err := json.Unmarshal([]byte(output[start:end+1]), &v); err == nil && v.Narrative != "" {
			return strings.TrimSpace(v.Narrative)
		}
	}
	return strings.TrimSpace(output)
}

// ValidateNarrative applies the quality gates required by step 12:
// non-empty, >= 100 chars, references at least 2 module names from the
// cluster, and does not contain forbidden template phrases. Returns nil
// on success, error describing the failure otherwise.
func ValidateNarrative(narrative string, cluster []ModuleGraph) error {
	if len(narrative) < 100 {
		return fmt.Errorf("narrative too short (%d chars)", len(narrative))
	}
	forbidden := []string{
		"this subsystem contains modules for",
		"this section describes",
		"lorem ipsum",
	}
	low := strings.ToLower(narrative)
	for _, p := range forbidden {
		if strings.Contains(low, p) {
			return fmt.Errorf("narrative contains forbidden template phrase %q", p)
		}
	}
	refs := 0
	for _, g := range cluster {
		// Match on the last path segment (leaf module name) since paths
		// are unwieldy in prose but leaf names show up naturally.
		leaf := g.Module
		if i := strings.LastIndex(leaf, "/"); i >= 0 {
			leaf = leaf[i+1:]
		}
		if leaf == "" {
			continue
		}
		if strings.Contains(narrative, leaf) {
			refs++
			if refs >= 2 {
				return nil
			}
		}
	}
	return fmt.Errorf("narrative references %d/2 required module names", refs)
}

// StubNarrative is the visible fallback rendered when an LLM cluster
// call fails validation. It's deliberately shaped to be noticeable in
// review, not hidden as silent noise.
func StubNarrative(subsystem string) string {
	return fmt.Sprintf("_(subsystem narrative for `%s` is unavailable — the model output failed quality gates. Re-run `golem graph build` to retry.)_", subsystem)
}
