package graph

import (
	"embed"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries/*.scm
var queryFS embed.FS

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
	".tsx":  "typescript",
	".js":   "javascript",
	".jsx":  "javascript",
	".mjs":  "javascript",
	".rs":   "rust",
	".java": "java",
}

// LangForExt returns the language name for a file extension, or "" if unknown.
func LangForExt(ext string) string {
	return extToLang[ext]
}

func tsLanguage(name string) *sitter.Language {
	switch name {
	case "golang":
		return sitter.NewLanguage(golang.Language())
	case "python":
		return sitter.NewLanguage(python.Language())
	case "typescript":
		return sitter.NewLanguage(typescript.LanguageTypescript())
	case "javascript":
		return sitter.NewLanguage(javascript.Language())
	case "rust":
		return sitter.NewLanguage(rust.Language())
	case "java":
		return sitter.NewLanguage(java.Language())
	}
	return nil
}

// Extract runs tree-sitter parsing against source to extract imports and
// exported symbols. Returns empty StructuralData (not an error) for unknown
// languages. Languages with an extended extractor produce doc comments,
// signatures, type fields, and consts; other languages fall back to the
// generic name-only query path.
func Extract(source []byte, langName string) (*StructuralData, error) {
	lang := tsLanguage(langName)
	if lang == nil {
		return &StructuralData{}, nil
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		return nil, err
	}
	tree := parser.Parse(source, nil)
	defer tree.Close()
	root := tree.RootNode()

	switch langName {
	case "golang":
		return extractGo(source, root), nil
	}

	// Generic fallback: single .scm query, name-only captures.
	return extractGeneric(source, lang, langName, root)
}

func extractGeneric(source []byte, lang *sitter.Language, langName string, root *sitter.Node) (*StructuralData, error) {
	queryBytes, err := queryFS.ReadFile("queries/" + langName + ".scm")
	if err != nil {
		return &StructuralData{}, nil
	}
	q, qErr := sitter.NewQuery(lang, string(queryBytes))
	if qErr != nil {
		return nil, qErr
	}
	defer q.Close()

	cursor := sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(q, root, source)
	captureNames := q.CaptureNames()

	d := &StructuralData{}
	seen := make(map[string]bool)

	for match := matches.Next(); match != nil; match = matches.Next() {
		if !match.SatisfiesTextPredicate(q, nil, nil, source) {
			continue
		}
		for _, cap := range match.Captures {
			name := captureNames[cap.Index]
			node := cap.Node
			text := strings.TrimSpace(string(source[node.StartByte():node.EndByte()]))
			text = strings.Trim(text, `"`)
			text = strings.TrimSpace(text)
			if text == "" || seen[name+":"+text] {
				continue
			}
			seen[name+":"+text] = true
			switch name {
			case "import":
				d.Imports = append(d.Imports, text)
			case "export_fn":
				d.ExportFns = append(d.ExportFns, text)
			case "export_type":
				d.ExportTypes = append(d.ExportTypes, text)
			}
		}
	}
	return d, nil
}

// collectDocAbove walks previous siblings of node while they are comment
// nodes and returns the joined doc-comment text with delimiters stripped.
// Comments are only associated when they are directly line-adjacent to the
// declaration (no blank line between). Returns "" if none.
func collectDocAbove(source []byte, node *sitter.Node, commentKind string, strip func(string) string) string {
	var lines []string
	prev := node.PrevSibling()
	lastStartRow := node.StartPosition().Row
	for prev != nil && prev.Kind() == commentKind {
		endRow := prev.EndPosition().Row
		if endRow+1 < lastStartRow {
			break
		}
		text := string(source[prev.StartByte():prev.EndByte()])
		lines = append([]string{strip(text)}, lines...)
		lastStartRow = prev.StartPosition().Row
		prev = prev.PrevSibling()
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

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

// extractGo produces a full StructuralData from a parsed Go source file
// using the tree-sitter Go grammar directly (no .scm query needed). It
// captures the package doc, imports, exported function signatures with
// docs, exported type fields with docs, and top-level const names.
func extractGo(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	cursor := root.Walk()
	defer cursor.Close()

	children := root.NamedChildren(cursor)

	// Package doc: comment(s) immediately preceding the package_clause.
	for i, c := range children {
		if c.Kind() == "package_clause" {
			d.PackageDoc = collectDocAbove(source, &children[i], "comment", stripGoComment)
			break
		}
	}

	for i := range children {
		c := &children[i]
		switch c.Kind() {
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

	// Populate name-only slices for downstream consumers.
	for _, f := range d.ExportedFuncs {
		d.ExportFns = append(d.ExportFns, f.Name)
	}
	for _, t := range d.ExportedTypes {
		d.ExportTypes = append(d.ExportTypes, t.Name)
	}
	return d
}

func extractGoImports(source []byte, decl *sitter.Node, d *StructuralData) {
	cursor := decl.Walk()
	defer cursor.Close()
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		child := decl.NamedChild(i)
		if child == nil {
			continue
		}
		switch child.Kind() {
		case "import_spec":
			addGoImport(source, child, d)
		case "import_spec_list":
			for j := uint(0); j < child.NamedChildCount(); j++ {
				spec := child.NamedChild(j)
				if spec != nil && spec.Kind() == "import_spec" {
					addGoImport(source, spec, d)
				}
			}
		}
	}
}

func addGoImport(source []byte, spec *sitter.Node, d *StructuralData) {
	path := spec.ChildByFieldName("path")
	if path == nil {
		return
	}
	text := strings.Trim(string(source[path.StartByte():path.EndByte()]), `"`)
	if text != "" {
		d.Imports = append(d.Imports, text)
	}
}

func extractGoFunc(source []byte, decl *sitter.Node, d *StructuralData) {
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	if !isExportedGo(nm) {
		return
	}
	// Signature = text from decl start to body start (exclusive).
	body := decl.ChildByFieldName("body")
	end := decl.EndByte()
	if body != nil {
		end = body.StartByte()
	}
	sig := strings.TrimSpace(string(source[decl.StartByte():end]))
	// Collapse whitespace runs.
	sig = strings.Join(strings.Fields(sig), " ")
	doc := collectDocAbove(source, decl, "comment", stripGoComment)
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: doc})
}

func extractGoTypes(source []byte, decl *sitter.Node, d *StructuralData) {
	// A type_declaration contains one or more type_spec children.
	docForDecl := collectDocAbove(source, decl, "comment", stripGoComment)
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		spec := decl.NamedChild(i)
		if spec == nil || spec.Kind() != "type_spec" {
			continue
		}
		name := spec.ChildByFieldName("name")
		if name == nil {
			continue
		}
		nm := string(source[name.StartByte():name.EndByte()])
		if !isExportedGo(nm) {
			continue
		}
		typeNode := spec.ChildByFieldName("type")
		fields := extractGoStructFields(source, typeNode)
		d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: docForDecl, Fields: fields})
	}
}

func extractGoStructFields(source []byte, typeNode *sitter.Node) []string {
	if typeNode == nil || typeNode.Kind() != "struct_type" {
		return nil
	}
	var out []string
	// struct_type -> field_declaration_list -> field_declaration*
	for i := uint(0); i < typeNode.NamedChildCount(); i++ {
		list := typeNode.NamedChild(i)
		if list == nil || list.Kind() != "field_declaration_list" {
			continue
		}
		for j := uint(0); j < list.NamedChildCount(); j++ {
			fd := list.NamedChild(j)
			if fd == nil || fd.Kind() != "field_declaration" {
				continue
			}
			text := strings.TrimSpace(string(source[fd.StartByte():fd.EndByte()]))
			text = strings.Join(strings.Fields(text), " ")
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func extractGoConsts(source []byte, decl *sitter.Node, d *StructuralData) {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		spec := decl.NamedChild(i)
		if spec == nil || spec.Kind() != "const_spec" {
			continue
		}
		name := spec.ChildByFieldName("name")
		if name == nil {
			continue
		}
		nm := string(source[name.StartByte():name.EndByte()])
		if isExportedGo(nm) {
			d.Consts = append(d.Consts, nm)
		}
	}
}

func isExportedGo(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return c >= 'A' && c <= 'Z'
}
