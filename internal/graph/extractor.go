package graph

import (
	"fmt"
	"go/token"
	"strings"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	ruby "github.com/tree-sitter/tree-sitter-ruby/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// StructuralData holds the tree-sitter-extracted structural information for a
// source file. `ExportFns` / `ExportTypes` remain as name-only slices for
// backward compatibility with callers that only need names; richer records
// are in `ExportedFuncs` / `ExportedTypes`.
type StructuralData struct {
	PackageDoc    string
	Imports       []string
	Consts        []string
	ExportFns     []string
	ExportTypes   []string
	ExportedFuncs []ExportedFunc
	ExportedTypes []ExportedType
}

var extToLang = map[string]string{
	".go":   "golang",
	".py":   "python",
	".ts":   "typescript",
	".tsx":  "tsx",
	".js":   "javascript",
	".jsx":  "javascript",
	".mjs":  "javascript",
	".rs":   "rust",
	".java": "java",
	".rb":   "ruby",
}

// LangForExt returns the language name for a file extension, or "" if unknown.
func LangForExt(ext string) string {
	return extToLang[ext]
}

// langSpec pairs a tree-sitter grammar with the extractor that walks it.
type langSpec struct {
	grammar func() unsafe.Pointer
	extract func(source []byte, root *sitter.Node) *StructuralData
}

var languages = map[string]langSpec{
	"golang":     {golang.Language, extractGo},
	"python":     {python.Language, extractPython},
	"typescript": {typescript.LanguageTypescript, extractJSTS},
	"tsx":        {typescript.LanguageTSX, extractJSTS},
	"javascript": {javascript.Language, extractJSTS},
	"rust":       {rust.Language, extractRust},
	"java":       {java.Language, extractJava},
	"ruby":       {ruby.Language, extractRuby},
}

// Extract parses source with the tree-sitter grammar for langName and
// returns its package doc, imports, consts, and exported functions/types
// (with signatures, docs, and fields). Returns empty StructuralData (not an
// error) for unknown languages.
func Extract(source []byte, langName string) (*StructuralData, error) {
	spec, ok := languages[langName]
	if !ok {
		return &StructuralData{}, nil
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(spec.grammar())); err != nil {
		return nil, err
	}
	tree := parser.Parse(source, nil)
	if tree == nil {
		return nil, fmt.Errorf("graph: parsing %s source failed", langName)
	}
	defer tree.Close()

	d := spec.extract(source, tree.RootNode())
	for _, f := range d.ExportedFuncs {
		d.ExportFns = append(d.ExportFns, f.Name)
	}
	for _, t := range d.ExportedTypes {
		d.ExportTypes = append(d.ExportTypes, t.Name)
	}
	return d, nil
}

// nodeText returns the source text spanned by n.
func nodeText(source []byte, n *sitter.Node) string {
	return string(source[n.StartByte():n.EndByte()])
}

// collapse trims s and folds whitespace runs to single spaces.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// fieldText returns the collapsed text of n's named field, or "".
func fieldText(source []byte, n *sitter.Node, field string) string {
	f := n.ChildByFieldName(field)
	if f == nil {
		return ""
	}
	return collapse(nodeText(source, f))
}

// childrenByField returns every child of n in the named field.
func childrenByField(n *sitter.Node, field string) []sitter.Node {
	cursor := n.Walk()
	defer cursor.Close()
	return n.ChildrenByFieldName(field, cursor)
}

// signatureBefore returns the collapsed text from the start of from up to
// (excluding) body, or all of from when body is nil.
func signatureBefore(source []byte, from, body *sitter.Node) string {
	end := from.EndByte()
	if body != nil {
		end = body.StartByte()
	}
	return collapse(string(source[from.StartByte():end]))
}

// docStyle describes how collectDocAbove recognises a language's doc comments.
type docStyle struct {
	// line returns the stripped text of a comment sibling, or ok=false to
	// end the doc run.
	line func(kind, text string) (doc string, ok bool)
	// skip reports siblings that may sit between a doc and its declaration
	// (e.g. Rust attributes). May be nil.
	skip func(kind string) bool
}

// collectDocAbove walks previous siblings of node and returns the joined
// doc-comment text. Comments are only associated when line-adjacent to the
// declaration (no blank line between). Returns "" if none.
func collectDocAbove(source []byte, node *sitter.Node, st docStyle) string {
	var lines []string
	lastStartRow := node.StartPosition().Row
	for prev := node.PrevSibling(); prev != nil; prev = prev.PrevSibling() {
		if prev.EndPosition().Row+1 < lastStartRow {
			break
		}
		lastStartRow = prev.StartPosition().Row
		if st.skip != nil && st.skip(prev.Kind()) {
			continue
		}
		doc, ok := st.line(prev.Kind(), nodeText(source, prev))
		if !ok {
			break
		}
		lines = append([]string{doc}, lines...)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

var goDoc = docStyle{line: func(kind, text string) (string, bool) {
	return stripGoComment(text), kind == "comment"
}}

// stripGoComment strips "// " or "/* ... */" delimiters from a Go comment.
func stripGoComment(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "//") {
		return strings.TrimSpace(strings.TrimPrefix(s, "//"))
	}
	if strings.HasPrefix(s, "/*") && strings.HasSuffix(s, "*/") {
		return strings.TrimSpace(s[2 : len(s)-2])
	}
	return s
}

// extractGo captures the package doc, imports, exported function signatures
// with docs, exported types with docs and exported fields/methods, and
// exported const names from a parsed Go source file.
func extractGo(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		switch c.Kind() {
		case "package_clause":
			d.PackageDoc = collectDocAbove(source, c, goDoc)
		case "import_declaration":
			extractGoImports(source, c, d)
		case "function_declaration", "method_declaration":
			extractGoFunc(source, c, d)
		case "type_declaration":
			extractGoTypes(source, c, d)
		case "const_declaration":
			extractGoConsts(source, c, d)
		}
	}
	return d
}

func extractGoImports(source []byte, decl *sitter.Node, d *StructuralData) {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		child := decl.NamedChild(i)
		switch child.Kind() {
		case "import_spec":
			addGoImport(source, child, d)
		case "import_spec_list":
			for j := uint(0); j < child.NamedChildCount(); j++ {
				if spec := child.NamedChild(j); spec.Kind() == "import_spec" {
					addGoImport(source, spec, d)
				}
			}
		}
	}
}

func addGoImport(source []byte, spec *sitter.Node, d *StructuralData) {
	if text := strings.Trim(fieldText(source, spec, "path"), "\"`"); text != "" {
		d.Imports = append(d.Imports, text)
	}
}

func extractGoFunc(source []byte, decl *sitter.Node, d *StructuralData) {
	nm := fieldText(source, decl, "name")
	if !token.IsExported(nm) {
		return
	}
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{
		Name:      nm,
		Signature: signatureBefore(source, decl, decl.ChildByFieldName("body")),
		Doc:       collectDocAbove(source, decl, goDoc),
	})
}

// goSpecs returns the specs of a type/const declaration, whether written
// singly or as a parenthesised group.
func goSpecs(decl *sitter.Node, kind string) []*sitter.Node {
	var specs []*sitter.Node
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		c := decl.NamedChild(i)
		switch c.Kind() {
		case kind:
			specs = append(specs, c)
		case kind + "_list": // newer grammars wrap groups in a list node
			for j := uint(0); j < c.NamedChildCount(); j++ {
				if s := c.NamedChild(j); s.Kind() == kind {
					specs = append(specs, s)
				}
			}
		}
	}
	return specs
}

func extractGoTypes(source []byte, decl *sitter.Node, d *StructuralData) {
	specs := goSpecs(decl, "type_spec")
	for _, spec := range specs {
		nm := fieldText(source, spec, "name")
		if !token.IsExported(nm) {
			continue
		}
		// Each grouped spec carries its own doc; the declaration's doc only
		// applies when it documents a single spec.
		doc := collectDocAbove(source, spec, goDoc)
		if doc == "" && len(specs) == 1 {
			doc = collectDocAbove(source, decl, goDoc)
		}
		d.ExportedTypes = append(d.ExportedTypes, ExportedType{
			Name:   nm,
			Doc:    doc,
			Fields: extractGoTypeMembers(source, spec.ChildByFieldName("type")),
		})
	}
}

// extractGoTypeMembers returns exported struct fields or interface method
// set entries (including embedded exported types).
func extractGoTypeMembers(source []byte, typeNode *sitter.Node) []string {
	if typeNode == nil {
		return nil
	}
	var out []string
	switch typeNode.Kind() {
	case "struct_type":
		for i := uint(0); i < typeNode.NamedChildCount(); i++ {
			list := typeNode.NamedChild(i)
			if list.Kind() != "field_declaration_list" {
				continue
			}
			for j := uint(0); j < list.NamedChildCount(); j++ {
				if fd := list.NamedChild(j); fd.Kind() == "field_declaration" {
					if f := goExportedField(source, fd); f != "" {
						out = append(out, f)
					}
				}
			}
		}
	case "interface_type":
		for i := uint(0); i < typeNode.NamedChildCount(); i++ {
			m := typeNode.NamedChild(i)
			switch m.Kind() {
			case "method_elem":
				if token.IsExported(fieldText(source, m, "name")) {
					out = append(out, collapse(nodeText(source, m)))
				}
			case "type_elem":
				if text := collapse(nodeText(source, m)); token.IsExported(goEmbeddedName(text)) {
					out = append(out, text)
				}
			}
		}
	}
	return out
}

// goExportedField renders a struct field declaration keeping only its
// exported names, or "" when none are exported.
func goExportedField(source []byte, fd *sitter.Node) string {
	names := childrenByField(fd, "name")
	if len(names) == 0 { // embedded field
		text := collapse(nodeText(source, fd))
		if token.IsExported(goEmbeddedName(text)) {
			return text
		}
		return ""
	}
	var keep []string
	for i := range names {
		if nm := nodeText(source, &names[i]); token.IsExported(nm) {
			keep = append(keep, nm)
		}
	}
	if len(keep) == 0 {
		return ""
	}
	rest := ""
	if t := fd.ChildByFieldName("type"); t != nil {
		rest = collapse(string(source[t.StartByte():fd.EndByte()]))
	}
	return strings.Join(keep, ", ") + " " + rest
}

// goEmbeddedName returns the type name of an embedded field or interface
// element, e.g. "*pkg.Name[T]" -> "Name".
func goEmbeddedName(text string) string {
	text = strings.TrimLeft(text, "*~")
	if i := strings.IndexAny(text, "[ "); i >= 0 {
		text = text[:i]
	}
	if i := strings.LastIndex(text, "."); i >= 0 {
		text = text[i+1:]
	}
	return text
}

func extractGoConsts(source []byte, decl *sitter.Node, d *StructuralData) {
	for _, spec := range goSpecs(decl, "const_spec") {
		for _, n := range childrenByField(spec, "name") {
			if n.Kind() != "identifier" {
				continue
			}
			if nm := nodeText(source, &n); token.IsExported(nm) {
				d.Consts = append(d.Consts, nm)
			}
		}
	}
}
