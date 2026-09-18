package graph

import (
	"path/filepath"
	"strings"
)

// subsystemSkip lists path segments that are transparent to the tagger:
// build scaffolding and common vendor/monorepo roots.
var subsystemSkip = map[string]bool{
	"internal":    true,
	"cmd":         true,
	"pkg":         true,
	"src":         true,
	"lib":         true,
	"staging":     true,
	"third_party": true,
	"vendor":      true,
	"packages":    true,
}

// DefaultCoarsenClusterSize caps a single subsystem's module count
// before coarsening splits it.
const DefaultCoarsenClusterSize = 200

// meaningfulSegments splits modulePath and drops scaffolding and
// host-like segments (containing '.', e.g. "k8s.io", "github.com").
func meaningfulSegments(modulePath string) []string {
	if modulePath == "" || modulePath == "." {
		return nil
	}
	// Both separators, not just the host's. filepath.ToSlash is a no-op on
	// Unix, so a Windows-style path arrived here as one long segment and
	// SubsystemForPath("internal\\graph") returned it whole — the test for
	// that case passed only on Windows. A module path is a directory path,
	// and normalising both is what every caller means.
	normalized := strings.ReplaceAll(filepath.ToSlash(modulePath), "\\", "/")
	raw := strings.Split(normalized, "/")
	out := make([]string, 0, len(raw))
	for _, seg := range raw {
		if seg == "" || seg == "." || subsystemSkip[seg] || strings.Contains(seg, ".") {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// SubsystemForPath returns the first meaningful segment of a module
// path, or "misc" if none. Example: "pkg/orchestrator/api" → "orchestrator".
func SubsystemForPath(modulePath string) string {
	segs := meaningfulSegments(modulePath)
	if len(segs) == 0 {
		return "misc"
	}
	return segs[0]
}

// subsystemAtDepth joins the first depth+1 meaningful segments (e.g.
// depth=1 → "api/core"). Truncates if the path is shallower.
func subsystemAtDepth(modulePath string, depth int) string {
	segs := meaningfulSegments(modulePath)
	if len(segs) == 0 {
		return "misc"
	}
	end := depth + 1
	if end > len(segs) {
		end = len(segs)
	}
	return strings.Join(segs[:end], "/")
}

// CoarsenSubsystems splits any cluster above maxClusterSize by
// descending one path level at a time. Modules whose path can't
// descend further keep their tag.
func CoarsenSubsystems(graphs []ModuleGraph, maxClusterSize int) []ModuleGraph {
	if maxClusterSize < 1 {
		return graphs
	}
	depths := make([]int, len(graphs))

	for pass := 0; pass < 8; pass++ {
		sizes := map[string]int{}
		tags := make([]string, len(graphs))
		for i, g := range graphs {
			tag := subsystemAtDepth(g.Module, depths[i])
			tags[i] = tag
			sizes[tag]++
		}
		oversized := false
		for _, sz := range sizes {
			if sz > maxClusterSize {
				oversized = true
				break
			}
		}
		if !oversized {
			for i := range graphs {
				graphs[i].Subsystem = tags[i]
			}
			return graphs
		}
		progressed := false
		for i, g := range graphs {
			if sizes[tags[i]] <= maxClusterSize {
				continue
			}
			if depths[i]+1 < len(meaningfulSegments(g.Module)) {
				depths[i]++
				progressed = true
			}
		}
		if !progressed {
			for i := range graphs {
				graphs[i].Subsystem = tags[i]
			}
			return graphs
		}
	}
	// Depth cap hit.
	for i, g := range graphs {
		graphs[i].Subsystem = subsystemAtDepth(g.Module, depths[i])
	}
	return graphs
}
