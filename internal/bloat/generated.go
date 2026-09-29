package bloat

import (
	"path"
	"strings"
)

// lockfileNames are dependency lockfiles, rewritten wholesale by tooling.
var lockfileNames = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"pnpm-lock.yaml":    true,
	"yarn.lock":         true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
	"uv.lock":           true,
}

// generatedSuffixes are protobuf/gRPC codegen outputs and test snapshots.
var generatedSuffixes = []string{
	".pb.go",
	"_pb2.py",
	"_pb2.pyi",
	"_pb2_grpc.py",
	".snap",
}

// IsGenerated reports whether a repo-relative path is a machine-generated
// artifact (lockfile, codegen output, Pact contract, test snapshot) whose
// size says nothing about the scope of the hand-written change. Plan steps
// estimate hand-written lines ("~150 lines + 1 generated JSON fixture"), so
// the SCOPE_BLOAT check must not count these against the expectation.
// Repo-specific generated paths belong in .gitattributes as
// linguist-generated, which the caller honours separately.
func IsGenerated(p string) bool {
	base := path.Base(p)
	if lockfileNames[base] {
		return true
	}
	for _, s := range generatedSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return path.Base(path.Dir(p)) == "pacts" && path.Ext(base) == ".json"
}
