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

// ModuleGraph is the per-module record persisted to
// `.golem/index/graph-modules/<slug>.json` and rendered to wiki pages.
// Every field except Subsystem comes from tree-sitter extraction; no LLM
// runs per module.
//
// Summary is only read from indexes written before extraction replaced
// per-module LLM prose; new records leave it empty and use PackageDoc.
// ExportFns/ExportTypes mirror the names in ExportedFuncs/ExportedTypes
// for readers that only need names (graphdeps, whoimports, symbols.md).
type ModuleGraph struct {
	Module     string   `json:"module"`
	Summary    string   `json:"summary,omitempty"`
	PackageDoc string   `json:"package_doc,omitempty"`
	Subsystem  string   `json:"subsystem,omitempty"`
	Imports    []string `json:"imports,omitempty"`
	Consts     []string `json:"consts,omitempty"`

	ExportedFuncs []ExportedFunc `json:"exported_funcs,omitempty"`
	ExportedTypes []ExportedType `json:"exported_types,omitempty"`

	ExportFns   []string `json:"export_fns,omitempty"`
	ExportTypes []string `json:"export_types,omitempty"`
}

// summary is the one-paragraph description shown for the module.
func (g ModuleGraph) summary() string {
	if g.PackageDoc != "" {
		return g.PackageDoc
	}
	return g.Summary
}
