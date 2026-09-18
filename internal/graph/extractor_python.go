package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// extractPython walks a parsed Python module. Docstrings (leading string
// literal in a module/function/class body) are used as doc text.
func extractPython(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	docSeen := false
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		// Module docstring: first statement after any shebang/comments.
		if !docSeen && c.Kind() != "comment" {
			docSeen = true
			d.PackageDoc = pythonDocstring(source, c)
		}
		if c.Kind() == "decorated_definition" {
			if def := c.ChildByFieldName("definition"); def != nil {
				c = def
			}
		}
		switch c.Kind() {
		case "import_statement":
			extractPyImportSimple(source, c, d)
		case "import_from_statement":
			if text := fieldText(source, c, "module_name"); text != "" {
				d.Imports = append(d.Imports, text)
			}
		case "function_definition":
			extractPyFunc(source, c, d)
		case "class_definition":
			extractPyClass(source, c, d)
		case "expression_statement":
			extractPyTopLevelAssign(source, c, d)
		}
	}
	return d
}

// pythonDocstring returns the string value of an expression_statement that
// contains only a string literal, or "" otherwise.
func pythonDocstring(source []byte, stmt *sitter.Node) string {
	if stmt == nil || stmt.Kind() != "expression_statement" || stmt.NamedChildCount() != 1 {
		return ""
	}
	child := stmt.NamedChild(0)
	if child.Kind() != "string" {
		return ""
	}
	for i := uint(0); i < child.NamedChildCount(); i++ {
		if part := child.NamedChild(i); part.Kind() == "string_content" {
			return strings.TrimSpace(nodeText(source, part))
		}
	}
	return strings.Trim(strings.TrimSpace(nodeText(source, child)), `"'`)
}

// pyBodyDocstring returns the docstring of a function/class body, or "".
func pyBodyDocstring(source []byte, body *sitter.Node) string {
	if body == nil || body.NamedChildCount() == 0 {
		return ""
	}
	return pythonDocstring(source, body.NamedChild(0))
}

func extractPyImportSimple(source []byte, decl *sitter.Node, d *StructuralData) {
	// import a, b.c
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		child := decl.NamedChild(i)
		if child.Kind() == "dotted_name" || child.Kind() == "aliased_import" {
			if text := collapse(nodeText(source, child)); text != "" {
				d.Imports = append(d.Imports, text)
			}
		}
	}
}

func extractPyFunc(source []byte, decl *sitter.Node, d *StructuralData) {
	nm := fieldText(source, decl, "name")
	if nm == "" || isPrivatePython(nm) {
		return
	}
	body := decl.ChildByFieldName("body")
	sig := strings.TrimSpace(strings.TrimSuffix(signatureBefore(source, decl, body), ":"))
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: pyBodyDocstring(source, body)})
}

func extractPyClass(source []byte, decl *sitter.Node, d *StructuralData) {
	nm := fieldText(source, decl, "name")
	if nm == "" || isPrivatePython(nm) {
		return
	}
	body := decl.ChildByFieldName("body")
	var fields []string
	if body != nil {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			stmt := body.NamedChild(i)
			if stmt.Kind() == "expression_statement" && stmt.NamedChildCount() == 1 &&
				stmt.NamedChild(0).Kind() == "assignment" {
				fields = append(fields, collapse(nodeText(source, stmt)))
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: pyBodyDocstring(source, body), Fields: fields})
}

func extractPyTopLevelAssign(source []byte, stmt *sitter.Node, d *StructuralData) {
	if stmt.NamedChildCount() != 1 {
		return
	}
	inner := stmt.NamedChild(0)
	if inner.Kind() != "assignment" {
		return
	}
	left := inner.ChildByFieldName("left")
	if left == nil || left.Kind() != "identifier" {
		return
	}
	nm := nodeText(source, left)
	// Only ALL_CAPS module-level names are treated as consts.
	if !isPrivatePython(nm) && strings.ToUpper(nm) == nm {
		d.Consts = append(d.Consts, nm)
	}
}

func isPrivatePython(name string) bool {
	return strings.HasPrefix(name, "_")
}
