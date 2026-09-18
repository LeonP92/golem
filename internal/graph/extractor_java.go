package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// javaDoc accepts only `/** */` Javadoc blocks, so license headers and
// plain comments are never attached to declarations.
var javaDoc = docStyle{line: func(kind, text string) (string, bool) {
	if kind != "block_comment" || !strings.HasPrefix(text, "/**") {
		return "", false
	}
	return stripJSDoc(text), true
}}

// extractJava walks a parsed Java compilation unit. PackageDoc is the
// Javadoc on the package declaration (package-info.java) or, failing that,
// the Javadoc of the first public top-level type.
func extractJava(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		switch c.Kind() {
		case "package_declaration":
			d.PackageDoc = collectDocAbove(source, c, javaDoc)
		case "import_declaration":
			if c.NamedChildCount() > 0 {
				if text := collapse(nodeText(source, c.NamedChild(0))); text != "" {
					d.Imports = append(d.Imports, text)
				}
			}
		case "class_declaration", "interface_declaration", "enum_declaration", "record_declaration":
			first := len(d.ExportedTypes) == 0
			extractJavaType(source, c, d)
			if first && d.PackageDoc == "" && len(d.ExportedTypes) > 0 {
				d.PackageDoc = d.ExportedTypes[0].Doc
			}
		}
	}
	return d
}

// extractJavaType records a public class/interface/enum/record with its
// public fields (interface: method signatures; enum: constants; record:
// components) and adds its public methods to ExportedFuncs.
func extractJavaType(source []byte, decl *sitter.Node, d *StructuralData) {
	nm := fieldText(source, decl, "name")
	if nm == "" || !javaHasModifier(source, decl, "public") {
		return
	}
	var fields []string
	if params := decl.ChildByFieldName("parameters"); params != nil { // record components
		for i := uint(0); i < params.NamedChildCount(); i++ {
			fields = append(fields, collapse(nodeText(source, params.NamedChild(i))))
		}
	}
	isInterface := decl.Kind() == "interface_declaration"
	var walk func(body *sitter.Node)
	walk = func(body *sitter.Node) {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			ch := body.NamedChild(i)
			switch ch.Kind() {
			case "enum_constant":
				fields = append(fields, fieldText(source, ch, "name"))
			case "enum_body_declarations":
				walk(ch)
			case "field_declaration", "constant_declaration":
				if isInterface || javaHasModifier(source, ch, "public") {
					fields = append(fields, collapse(nodeText(source, ch)))
				}
			case "method_declaration":
				if isInterface {
					fields = append(fields, collapse(nodeText(source, ch)))
				} else if javaHasModifier(source, ch, "public") {
					extractJavaMethod(source, ch, d)
				}
			}
		}
	}
	if body := decl.ChildByFieldName("body"); body != nil {
		walk(body)
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: collectDocAbove(source, decl, javaDoc), Fields: fields})
}

func extractJavaMethod(source []byte, decl *sitter.Node, d *StructuralData) {
	nm := fieldText(source, decl, "name")
	if nm == "" {
		return
	}
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{
		Name:      nm,
		Signature: signatureBefore(source, decl, decl.ChildByFieldName("body")),
		Doc:       collectDocAbove(source, decl, javaDoc),
	})
}

func javaHasModifier(source []byte, decl *sitter.Node, want string) bool {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		ch := decl.NamedChild(i)
		if ch.Kind() != "modifiers" {
			continue
		}
		for _, tok := range strings.Fields(nodeText(source, ch)) {
			if tok == want {
				return true
			}
		}
	}
	return false
}
