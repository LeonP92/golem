package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// extractPython walks a parsed Python module using the tree-sitter Python
// grammar directly. Docstrings (leading string literal in a module/
// function/class body) are used as doc text.
func extractPython(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	cursor := root.Walk()
	defer cursor.Close()
	children := root.NamedChildren(cursor)

	// Module docstring: first statement is expression_statement whose only
	// child is a string.
	if len(children) > 0 {
		if s := pythonDocstring(source, &children[0]); s != "" {
			d.PackageDoc = s
		}
	}

	for i := range children {
		c := &children[i]
		switch c.Kind() {
		case "import_statement":
			extractPyImportSimple(source, c, d)
		case "import_from_statement":
			extractPyImportFrom(source, c, d)
		case "function_definition":
			extractPyFunc(source, c, d)
		case "class_definition":
			extractPyClass(source, c, d)
		case "expression_statement":
			extractPyTopLevelAssign(source, c, d)
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

// pythonDocstring returns the string value of a leading expression_statement
// that contains only a string literal, or "" otherwise.
func pythonDocstring(source []byte, stmt *sitter.Node) string {
	if stmt.Kind() != "expression_statement" {
		return ""
	}
	if stmt.NamedChildCount() != 1 {
		return ""
	}
	child := stmt.NamedChild(0)
	if child == nil || child.Kind() != "string" {
		return ""
	}
	// Find string_content inside; fall back to raw text with quotes trimmed.
	for i := uint(0); i < child.NamedChildCount(); i++ {
		part := child.NamedChild(i)
		if part != nil && part.Kind() == "string_content" {
			return strings.TrimSpace(string(source[part.StartByte():part.EndByte()]))
		}
	}
	raw := string(source[child.StartByte():child.EndByte()])
	raw = strings.TrimSpace(raw)
	return strings.Trim(raw, `"'`)
}

func extractPyImportSimple(source []byte, decl *sitter.Node, d *StructuralData) {
	// import a, b.c
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		child := decl.NamedChild(i)
		if child == nil {
			continue
		}
		if child.Kind() == "dotted_name" || child.Kind() == "aliased_import" {
			addPyImport(source, child, d)
		}
	}
}

func extractPyImportFrom(source []byte, decl *sitter.Node, d *StructuralData) {
	// from x.y import z  -> record "x.y"
	mod := decl.ChildByFieldName("module_name")
	if mod != nil {
		text := strings.TrimSpace(string(source[mod.StartByte():mod.EndByte()]))
		if text != "" {
			d.Imports = append(d.Imports, text)
		}
	}
}

func addPyImport(source []byte, n *sitter.Node, d *StructuralData) {
	text := strings.TrimSpace(string(source[n.StartByte():n.EndByte()]))
	if text != "" {
		d.Imports = append(d.Imports, text)
	}
}

func extractPyFunc(source []byte, decl *sitter.Node, d *StructuralData) {
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	if isPrivatePython(nm) {
		return
	}
	body := decl.ChildByFieldName("body")
	end := decl.EndByte()
	if body != nil {
		end = body.StartByte()
	}
	sig := strings.TrimSpace(string(source[decl.StartByte():end]))
	sig = strings.Join(strings.Fields(sig), " ")
	sig = strings.TrimSuffix(sig, ":")
	sig = strings.TrimSpace(sig)
	doc := ""
	if body != nil && body.NamedChildCount() > 0 {
		first := body.NamedChild(0)
		if first != nil {
			doc = pythonDocstring(source, first)
		}
	}
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: doc})
}

func extractPyClass(source []byte, decl *sitter.Node, d *StructuralData) {
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	if isPrivatePython(nm) {
		return
	}
	body := decl.ChildByFieldName("body")
	doc := ""
	var fields []string
	if body != nil {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			stmt := body.NamedChild(i)
			if stmt == nil {
				continue
			}
			if i == 0 {
				if s := pythonDocstring(source, stmt); s != "" {
					doc = s
					continue
				}
			}
			if stmt.Kind() == "expression_statement" && stmt.NamedChildCount() == 1 {
				inner := stmt.NamedChild(0)
				if inner != nil && inner.Kind() == "assignment" {
					text := strings.TrimSpace(string(source[stmt.StartByte():stmt.EndByte()]))
					text = strings.Join(strings.Fields(text), " ")
					fields = append(fields, text)
				}
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: doc, Fields: fields})
}

func extractPyTopLevelAssign(source []byte, stmt *sitter.Node, d *StructuralData) {
	if stmt.NamedChildCount() != 1 {
		return
	}
	inner := stmt.NamedChild(0)
	if inner == nil || inner.Kind() != "assignment" {
		return
	}
	left := inner.ChildByFieldName("left")
	if left == nil || left.Kind() != "identifier" {
		return
	}
	nm := string(source[left.StartByte():left.EndByte()])
	if isPrivatePython(nm) {
		return
	}
	// Heuristic: treat ALL_CAPS or module-level assignments to identifiers as
	// consts. To avoid excessive noise, only record ALL_CAPS names.
	if strings.ToUpper(nm) == nm {
		d.Consts = append(d.Consts, nm)
	}
}

func isPrivatePython(name string) bool {
	return strings.HasPrefix(name, "_")
}
