package graph

// ExportedFunc is a single exported/public function extracted from source.
// Signature is language-appropriate (Go: "func Foo(x int) error"; Python:
// "def foo(x: int) -> None:"; etc.). Doc is the verbatim leading doc
// comment with delimiters stripped, or "" if none.
type ExportedFunc struct {
	Name      string `json:"name"`
	Signature string `json:"signature,omitempty"`
	Doc       string `json:"doc,omitempty"`
}

// ExportedType is a single exported/public type (Go: struct/interface;
// Python: class; TS: class/interface; etc.). Fields lists the type's
// fields/members verbatim as declared, or nil if the language/grammar
// does not expose them.
type ExportedType struct {
	Name   string   `json:"name"`
	Doc    string   `json:"doc,omitempty"`
	Fields []string `json:"fields,omitempty"`
}

// ModuleGraph is the per-module structural + narrative record persisted
// to `.golem/index/graph-modules/<slug>.json` and rendered to wiki pages.
//
// After the graph-deterministic rewrite, all fields except `Subsystem`
// come from tree-sitter extraction (verbatim doc comments, signatures,
// field lists). The LLM is invoked only per subsystem cluster, not per
// module.
//
// `Summary` is retained as a fallback: when `PackageDoc` is present it
// serves as the summary; when absent (languages without extended
// extraction), `Summary` may stay empty and the module page renders with
// symbol list only.
//
// `ExportFns` and `ExportTypes` are name-only slices kept populated for
// backward compatibility with downstream consumers (`graphdeps`,
// `whoimports`, `checkboundary`, and any external tooling reading the
// stored JSON). They mirror the `Name` field of `ExportedFuncs` /
// `ExportedTypes`.
type ModuleGraph struct {
	Module     string   `json:"module"`
	Summary    string   `json:"summary,omitempty"`
	PackageDoc string   `json:"package_doc,omitempty"`
	Subsystem  string   `json:"subsystem,omitempty"`
	Imports    []string `json:"imports,omitempty"`
	Consts     []string `json:"consts,omitempty"`

	ExportedFuncs []ExportedFunc `json:"exported_funcs,omitempty"`
	ExportedTypes []ExportedType `json:"exported_types,omitempty"`

	// Name-only slices kept populated from ExportedFuncs / ExportedTypes
	// for backward compatibility with existing readers.
	ExportFns   []string `json:"export_fns,omitempty"`
	ExportTypes []string `json:"export_types,omitempty"`
}

// PopulateNameSlices fills ExportFns/ExportTypes from ExportedFuncs/
// ExportedTypes. Callers building a ModuleGraph from tree-sitter data
// should invoke this once to keep the name slices in sync.
func (g *ModuleGraph) PopulateNameSlices() {
	if len(g.ExportedFuncs) > 0 {
		g.ExportFns = make([]string, 0, len(g.ExportedFuncs))
		for _, f := range g.ExportedFuncs {
			g.ExportFns = append(g.ExportFns, f.Name)
		}
	}
	if len(g.ExportedTypes) > 0 {
		g.ExportTypes = make([]string, 0, len(g.ExportedTypes))
		for _, t := range g.ExportedTypes {
			g.ExportTypes = append(g.ExportTypes, t.Name)
		}
	}
}
