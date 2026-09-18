package graph

import (
	"path/filepath"
	"strings"
)

// subsystemSkip contains path-segment names that are considered structural
// scaffolding (not meaningful subsystem tags). When the first segment of a
// module path is one of these, the second segment is used instead.
var subsystemSkip = map[string]bool{
	"internal": true,
	"cmd":      true,
	"pkg":      true,
	"src":      true,
	"lib":      true,
}

// SubsystemForPath derives a subsystem tag from a module path by walking
// path segments and returning the first non-scaffolding segment. Examples:
//
//	internal/graph        -> "graph"
//	internal/cli          -> "cli"
//	cmd/golem             -> "golem"
//	src/lib/auth          -> "auth"
//	pkg/orchestrator/api  -> "orchestrator"
//	.                     -> "misc"
//
// Returns "misc" when no meaningful segment is found (empty or all-skip
// path). Uses forward slashes internally; accepts either separator.
func SubsystemForPath(modulePath string) string {
	if modulePath == "" || modulePath == "." {
		return "misc"
	}
	p := filepath.ToSlash(modulePath)
	segments := strings.Split(p, "/")
	for _, seg := range segments {
		if seg == "" || seg == "." {
			continue
		}
		if subsystemSkip[seg] {
			continue
		}
		return seg
	}
	return "misc"
}
