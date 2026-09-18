package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

var jsDoc = docStyle{line: func(kind, text string) (string, bool) {
	return stripJSDoc(text), kind == "comment"
}}

// extractJSTS handles the JavaScript, TypeScript and TSX grammars — the node
// kinds relevant to us share the same names between the grammar families.
// Declarations are exported via `export <decl>` or a local `export { a, b }`.
func extractJSTS(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{PackageDoc: jsModuleDoc(source, root)}

	// Names exported via `export { a, b as c }` without a `from` clause.
	clauseExported := map[string]bool{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "export_statement" {
			continue
		}
		if src := c.ChildByFieldName("source"); src != nil {
			d.Imports = append(d.Imports, strings.Trim(nodeText(source, src), `"'`))
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			if clause := c.NamedChild(j); clause.Kind() == "export_clause" {
				for k := uint(0); k < clause.NamedChildCount(); k++ {
					if nm := fieldText(source, clause.NamedChild(k), "name"); nm != "" {
						clauseExported[nm] = true
					}
				}
			}
		}
	}

	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		switch c.Kind() {
		case "import_statement":
			if text := strings.Trim(fieldText(source, c, "source"), `"'`); text != "" {
				d.Imports = append(d.Imports, text)
			}
		case "export_statement":
			if inner := c.ChildByFieldName("declaration"); inner != nil {
				extractJSDecl(source, inner, c, d, nil)
			}
		default:
			extractJSDecl(source, c, c, d, clauseExported)
		}
	}
	return d
}

// jsModuleDoc returns the first leading `/** */` block, skipping license,
// eslint and `//` comments. A block that directly documents the first
// non-import statement belongs to that declaration instead.
func jsModuleDoc(source []byte, root *sitter.Node) string {
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() == "hash_bang_line" {
			continue
		}
		if c.Kind() != "comment" {
			return ""
		}
		text := nodeText(source, c)
		if !strings.HasPrefix(text, "/**") {
			continue
		}
		if next := c.NextNamedSibling(); next != nil && next.Kind() != "comment" &&
			next.Kind() != "import_statement" && next.StartPosition().Row <= c.EndPosition().Row+1 {
			return ""
		}
		return stripJSDoc(text)
	}
	return ""
}

// extractJSDecl records decl if exported. anchor is the top-level node
// used for doc lookup. When only is non-nil, just declarations whose name
// is in it are recorded (non-exported top-level statements); a nil only
// means decl is already exported.
func extractJSDecl(source []byte, decl, anchor *sitter.Node, d *StructuralData, only map[string]bool) {
	if decl.Kind() == "lexical_declaration" || decl.Kind() == "variable_declaration" {
		extractJSLexical(source, decl, anchor, d, only)
		return
	}
	nm := fieldText(source, decl, "name")
	if nm == "" || (only != nil && !only[nm]) {
		return
	}
	doc := collectDocAbove(source, anchor, jsDoc)
	body := decl.ChildByFieldName("body")
	switch decl.Kind() {
	case "function_declaration", "generator_function_declaration":
		d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: signatureBefore(source, decl, body), Doc: doc})
	case "class_declaration", "abstract_class_declaration":
		fields := jsMembers(source, body, "public_field_definition", "field_definition")
		d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: doc, Fields: fields})
	case "interface_declaration":
		fields := jsMembers(source, body, "property_signature", "method_signature")
		d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: doc, Fields: fields})
	case "type_alias_declaration":
		fields := jsMembers(source, decl.ChildByFieldName("value"), "property_signature", "method_signature")
		d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: doc, Fields: fields})
	case "enum_declaration":
		fields := jsMembers(source, body, "property_identifier", "enum_assignment")
		d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: doc, Fields: fields})
	}
}

// jsMembers returns the collapsed text of body's children of the given kinds.
func jsMembers(source []byte, body *sitter.Node, kinds ...string) []string {
	if body == nil {
		return nil
	}
	var out []string
	for i := uint(0); i < body.NamedChildCount(); i++ {
		ch := body.NamedChild(i)
		for _, k := range kinds {
			if ch.Kind() == k {
				out = append(out, collapse(nodeText(source, ch)))
			}
		}
	}
	return out
}

// extractJSLexical handles top-level const/let/var. `x = require('y')` is
// an import for any binding; exported bindings to function/arrow values
// are funcs and exported ALL_CAPS bindings are consts. Destructuring
// patterns are ignored.
func extractJSLexical(source []byte, decl, anchor *sitter.Node, d *StructuralData, only map[string]bool) {
	keyword := ""
	if kw := decl.Child(0); kw != nil {
		keyword = nodeText(source, kw)
	}
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		vd := decl.NamedChild(i)
		name := vd.ChildByFieldName("name")
		if vd.Kind() != "variable_declarator" || name == nil || name.Kind() != "identifier" {
			continue
		}
		nm := nodeText(source, name)
		value := vd.ChildByFieldName("value")

		if value != nil && value.Kind() == "call_expression" && fieldText(source, value, "function") == "require" {
			if args := value.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
				if text := strings.Trim(collapse(nodeText(source, args.NamedChild(0))), `"'`); text != "" {
					d.Imports = append(d.Imports, text)
				}
			}
			continue
		}
		if only != nil && !only[nm] {
			continue
		}

		if value != nil && (value.Kind() == "arrow_function" || value.Kind() == "function_expression") {
			sig := keyword + " " + signatureBefore(source, vd, value.ChildByFieldName("body"))
			d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: collectDocAbove(source, anchor, jsDoc)})
		} else if strings.ToUpper(nm) == nm {
			d.Consts = append(d.Consts, nm)
		}
	}
}

// stripJSDoc strips leading "/**", trailing "*/", per-line " * " markers,
// and "//" from a JS/TS/Java comment.
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
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
