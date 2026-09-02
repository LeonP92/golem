package roles

import "embed"

//go:embed defaults/*.md
var Defaults embed.FS

var RoleNames = []string{"developer", "convention-enforcer", "spec-adherence", "reviewer", "graph-builder"}
