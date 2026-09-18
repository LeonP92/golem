package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// extractJSTS handles both JavaScript and TypeScript grammars — the node
// kinds relevant to us (import_statement, export_statement, class/
// interface/function/lexical declarations) share the same names between
// the two grammar families.
func extractJSTS(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	cursor := root.Walk()
	defer cursor.Close()
	children := root.NamedChildren(cursor)

	// Module doc: first leading /** */ comment before any code.
	if len(children) > 0 && children[0].Kind() == "comment" {
		d.PackageDoc = stripJSDoc(string(source[children[0].StartByte():children[0].EndByte()]))
	}

	for i := range children {
		c := &children[i]
		switch c.Kind() {
		case "import_statement":
			extractJSImport(source, c, d)
		case "lexical_declaration", "variable_declaration":
			// May be `const x = require('...')` or `const X = ...`.
			extractJSLexical(source, c, d, false)
		case "export_statement":
			extractJSExport(source, c, d)
		case "function_declaration":
			extractJSFunc(source, c, d, false)
		case "class_declaration":
			extractJSClass(source, c, d, false)
		case "interface_declaration":
			extractJSInterface(source, c, d, false)
		}
	}

	for _, f := range d.ExportedFuncs {
		d.ExportFns = append(d.ExportFns, f.Name)
	}
	for _, t := range d.ExportedTypes {
		d.ExportTypes = append(d.ExportTypes, t.Name)
	}
	return d
}

func extractJSImport(source []byte, decl *sitter.Node, d *StructuralData) {
	src := decl.ChildByFieldName("source")
	if src == nil {
		return
	}
	text := strings.TrimSpace(string(source[src.StartByte():src.EndByte()]))
	text = strings.Trim(text, `"'`)
	if text != "" {
		d.Imports = append(d.Imports, text)
	}
}

// extractJSLexical handles top-level const/let/var. Detects
// `const x = require('y')` as an import; treats ALL_CAPS bindings as
// consts when `exported` is true (only exported names are recorded).
func extractJSLexical(source []byte, decl *sitter.Node, d *StructuralData, exported bool) {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		vd := decl.NamedChild(i)
		if vd == nil || vd.Kind() != "variable_declarator" {
			continue
		}
		name := vd.ChildByFieldName("name")
		value := vd.ChildByFieldName("value")
		if name == nil {
			continue
		}
		nm := string(source[name.StartByte():name.EndByte()])

		// require('x') detection.
		if value != nil && value.Kind() == "call_expression" {
			fn := value.ChildByFieldName("function")
			if fn != nil && string(source[fn.StartByte():fn.EndByte()]) == "require" {
				args := value.ChildByFieldName("arguments")
				if args != nil && args.NamedChildCount() > 0 {
					arg := args.NamedChild(0)
					if arg != nil {
						text := strings.Trim(strings.TrimSpace(string(source[arg.StartByte():arg.EndByte()])), `"'`)
						if text != "" {
							d.Imports = append(d.Imports, text)
						}
					}
				}
				continue
			}
		}

		if exported && strings.ToUpper(nm) == nm {
			d.Consts = append(d.Consts, nm)
		}
	}
}

func extractJSExport(source []byte, decl *sitter.Node, d *StructuralData) {
	inner := decl.ChildByFieldName("declaration")
	if inner == nil {
		return
	}
	switch inner.Kind() {
	case "function_declaration":
		extractJSFunc(source, inner, d, true)
	case "class_declaration":
		extractJSClass(source, inner, d, true)
	case "interface_declaration":
		extractJSInterface(source, inner, d, true)
	case "lexical_declaration", "variable_declaration":
		extractJSLexical(source, inner, d, true)
	}
	_ = decl
}

func extractJSFunc(source []byte, decl *sitter.Node, d *StructuralData, exported bool) {
	if !exported {
		return
	}
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	body := decl.ChildByFieldName("body")
	end := decl.EndByte()
	if body != nil {
		end = body.StartByte()
	}
	sig := strings.TrimSpace(string(source[decl.StartByte():end]))
	sig = strings.Join(strings.Fields(sig), " ")
	doc := jsDocAbove(source, findJSExportOrDecl(decl))
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: doc})
}

func extractJSClass(source []byte, decl *sitter.Node, d *StructuralData, exported bool) {
	if !exported {
		return
	}
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	body := decl.ChildByFieldName("body")
	var fields []string
	if body != nil {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			ch := body.NamedChild(i)
			if ch == nil {
				continue
			}
			if ch.Kind() == "public_field_definition" || ch.Kind() == "field_definition" {
				text := strings.TrimSpace(string(source[ch.StartByte():ch.EndByte()]))
				text = strings.Join(strings.Fields(text), " ")
				fields = append(fields, text)
			}
		}
	}
	doc := jsDocAbove(source, findJSExportOrDecl(decl))
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: doc, Fields: fields})
}

func extractJSInterface(source []byte, decl *sitter.Node, d *StructuralData, exported bool) {
	if !exported {
		return
	}
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	body := decl.ChildByFieldName("body")
	var fields []string
	if body != nil {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			ch := body.NamedChild(i)
			if ch == nil {
				continue
			}
			if ch.Kind() == "property_signature" {
				text := strings.TrimSpace(string(source[ch.StartByte():ch.EndByte()]))
				text = strings.Join(strings.Fields(text), " ")
				fields = append(fields, text)
			}
		}
	}
	doc := jsDocAbove(source, findJSExportOrDecl(decl))
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: doc, Fields: fields})
}

// findJSExportOrDecl returns the outermost export_statement if the decl is
// wrapped in one, otherwise the decl itself. Used to anchor doc-comment
// lookup at the top level of the module.
func findJSExportOrDecl(decl *sitter.Node) *sitter.Node {
	p := decl.Parent()
	if p != nil && p.Kind() == "export_statement" {
		return p
	}
	return decl
}

// jsDocAbove returns the stripped text of the /** ... */ or // comments
// immediately preceding node, or "".
func jsDocAbove(source []byte, node *sitter.Node) string {
	if node == nil {
		return ""
	}
	var acc []string
	prev := node.PrevSibling()
	lastRow := node.StartPosition().Row
	for prev != nil && prev.Kind() == "comment" {
		if prev.EndPosition().Row+1 < lastRow {
			break
		}
		text := stripJSDoc(string(source[prev.StartByte():prev.EndByte()]))
		acc = append([]string{text}, acc...)
		lastRow = prev.StartPosition().Row
		prev = prev.PrevSibling()
	}
	return strings.TrimSpace(strings.Join(acc, "\n"))
}

// stripJSDoc strips leading "/**", trailing "*/", per-line " * " markers,
// and "//" from a JS/TS comment.
func stripJSDoc(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "/**") && strings.HasSuffix(s, "*/") {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "/**"), "*/")
	} else if strings.HasPrefix(s, "/*") && strings.HasSuffix(s, "*/") {
		s = strings.TrimSuffix(strings.TrimPrefix(s, "/*"), "*/")
	} else if strings.HasPrefix(s, "//") {
		return strings.TrimSpace(strings.TrimPrefix(s, "//"))
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
