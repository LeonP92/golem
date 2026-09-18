package graph

import (
	"path/filepath"
	"strings"
)

// subsystemSkip contains path-segment names that are considered structural
// scaffolding (not meaningful subsystem tags). When walking a module path,
// these segments are transparent — the tagger looks past them.
var subsystemSkip = map[string]bool{
	"internal": true,
	"cmd":      true,
	"pkg":      true,
	"src":      true,
	"lib":      true,
	// Vendored-source conventions: kubernetes uses staging/src/*, Bazel
	// often uses third_party/*, JS monorepos use packages/*.
	"staging":     true,
	"third_party": true,
	"vendor":      true,
	"packages":    true,
}

// DefaultCoarsenClusterSize is the default max modules per subsystem
// cluster before coarsening kicks in. On kubernetes/kubernetes this
// takes the naive-tag 1701-module "staging" cluster and splits it into
// the meaningful api/apimachinery/client-go/... subclusters.
const DefaultCoarsenClusterSize = 200

// meaningfulSegments returns the ordered list of non-scaffolding, non-host
// segments in a module path. Host-like segments (containing '.') such as
// "k8s.io", "github.com", "sigs.k8s.io" are treated as transparent so
// that a path like `staging/src/k8s.io/api/core/v1` yields
// ["api", "core", "v1"].
func meaningfulSegments(modulePath string) []string {
	if modulePath == "" || modulePath == "." {
		return nil
	}
	p := filepath.ToSlash(modulePath)
	raw := strings.Split(p, "/")
	out := make([]string, 0, len(raw))
	for _, seg := range raw {
		if seg == "" || seg == "." {
			continue
		}
		if subsystemSkip[seg] {
			continue
		}
		if strings.Contains(seg, ".") {
			// Host-like ("k8s.io", "github.com"); a common vendored-src
			// convention — meaningful subsystem lives one level deeper.
			continue
		}
		out = append(out, seg)
	}
	return out
}

// SubsystemForPath derives a subsystem tag from a module path by walking
// path segments and returning the first meaningful one. Examples:
//
//	internal/graph                     -> "graph"
//	internal/cli                       -> "cli"
//	cmd/golem                          -> "golem"
//	src/lib/auth                       -> "auth"
//	pkg/orchestrator/api               -> "orchestrator"
//	staging/src/k8s.io/api/core/v1     -> "api"
//	.                                  -> "misc"
//
// Returns "misc" when no meaningful segment is found.
func SubsystemForPath(modulePath string) string {
	segs := meaningfulSegments(modulePath)
	if len(segs) == 0 {
		return "misc"
	}
	return segs[0]
}

// subsystemAtDepth returns the coarsened tag for a path at a given depth.
// depth==0 matches SubsystemForPath. depth==1 joins the first two
// meaningful segments ("api/core"), depth==2 joins three, etc. If the
// path doesn't have enough meaningful segments, returns the deepest
// available tag (no error).
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

// CoarsenSubsystems mutates each graph's Subsystem tag so no cluster
// exceeds maxClusterSize. Oversized clusters are split by descending
// one path level at a time (subsystemAtDepth). Modules whose path is
// too shallow to descend keep their current tag — bounded by the
// natural depth of the tree, not by iteration count.
//
// This is the fix for the kubernetes/kubernetes benchmark finding:
// staging/src/k8s.io/... collapsed 1701 modules into "staging". After
// one coarsening pass at DefaultCoarsenClusterSize=200, they redistribute
// across api/apimachinery/client-go/... — recovering the taxonomy.
func CoarsenSubsystems(graphs []ModuleGraph, maxClusterSize int) []ModuleGraph {
	if maxClusterSize < 1 {
		return graphs
	}
	// Track each module's current depth. Increment for modules in
	// oversized clusters until every cluster fits or every module
	// has hit its path's max meaningful depth.
	depths := make([]int, len(graphs))

	for pass := 0; pass < 8; pass++ { // safety cap on descent
		// Count cluster sizes at current depth assignment.
		sizes := map[string]int{}
		tags := make([]string, len(graphs))
		for i, g := range graphs {
			tag := subsystemAtDepth(g.Module, depths[i])
			tags[i] = tag
			sizes[tag]++
		}
		// Any cluster oversized?
		any := false
		for _, sz := range sizes {
			if sz > maxClusterSize {
				any = true
				break
			}
		}
		if !any {
			// Commit the current tag assignment and stop.
			for i := range graphs {
				graphs[i].Subsystem = tags[i]
			}
			return graphs
		}
		// Descend one level for every module in an oversized cluster —
		// but only if its path has room to descend.
		progressed := false
		for i, g := range graphs {
			if sizes[tags[i]] <= maxClusterSize {
				continue
			}
			segs := meaningfulSegments(g.Module)
			if depths[i]+1 < len(segs) {
				depths[i]++
				progressed = true
			}
		}
		if !progressed {
			// Every oversized cluster is composed of modules with no
			// deeper meaningful segments — accept the coarsening as-is.
			for i := range graphs {
				graphs[i].Subsystem = tags[i]
			}
			return graphs
		}
	}
	// Depth cap hit — commit whatever we have.
	for i, g := range graphs {
		graphs[i].Subsystem = subsystemAtDepth(g.Module, depths[i])
	}
	return graphs
}
