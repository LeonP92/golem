package graph

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// SubsystemNarrativeCache persists cluster narratives on disk keyed by
// ClusterHash, so unchanged clusters never reach the LLM. Rejected
// outputs are cached too (Failed) so incremental updates don't re-bill
// them on every run. Safe for concurrent use.
type SubsystemNarrativeCache struct {
	path    string
	mu      sync.Mutex
	entries map[string]narrativeEntry
}

type narrativeEntry struct {
	Subsystem string `json:"subsystem"`
	Narrative string `json:"narrative,omitempty"`
	Failed    bool   `json:"failed,omitempty"`
}

// LoadNarrativeCache reads the cache file at path. A missing file yields
// an empty cache; an unreadable one yields an empty cache and the error.
func LoadNarrativeCache(path string) (*SubsystemNarrativeCache, error) {
	c := &SubsystemNarrativeCache{path: path, entries: map[string]narrativeEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c.entries); err != nil {
		c.entries = map[string]narrativeEntry{}
		return c, err
	}
	return c, nil
}

// Lookup returns the cached narrative for hash. failed reports a cached
// quality-gate rejection; ok is false when hash is not cached at all.
func (c *SubsystemNarrativeCache) Lookup(hash string) (narrative string, failed, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[hash]
	return e.Narrative, e.Failed, ok
}

func (c *SubsystemNarrativeCache) Set(subsystem, hash, narrative string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[hash] = narrativeEntry{Subsystem: subsystem, Narrative: narrative}
}

// SetFailed records that the cluster at hash produced a rejected narrative.
func (c *SubsystemNarrativeCache) SetFailed(subsystem, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[hash] = narrativeEntry{Subsystem: subsystem, Failed: true}
}

// Prune drops every entry whose hash is not in keep, so superseded
// cluster versions don't accumulate.
func (c *SubsystemNarrativeCache) Prune(keep map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for h := range c.entries {
		if !keep[h] {
			delete(c.entries, h)
		}
	}
}

func (c *SubsystemNarrativeCache) Save() error {
	c.mu.Lock()
	data, err := json.MarshalIndent(c.entries, "", "  ")
	c.mu.Unlock()
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

// ValidateNarrative applies the narrative quality gates:
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
	// Required reference count scales with cluster size: single-module
	// clusters can only reference one name, so demanding >=2 would be
	// impossible. For clusters with 2+ modules, require >=2 refs.
	required := 2
	if len(cluster) < 2 {
		required = 1
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
			if refs >= required {
				return nil
			}
		}
	}
	return fmt.Errorf("narrative references %d/%d required module names", refs, required)
}

// StubNarrative is the visible fallback rendered when a cluster narrative
// cannot be produced. It is deliberately shaped to be noticeable in
// review, not hidden as silent noise.
func StubNarrative(subsystem string) string {
	return fmt.Sprintf("_(subsystem narrative for `%s` is unavailable — generation failed or was rejected. Re-run `golem graph build` to retry.)_", subsystem)
}
