package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// rustDoc collects `///` outer doc lines, looking through attributes such as
// #[derive(...)] that sit between the doc and the item.
var rustDoc = docStyle{
	line: func(kind, text string) (string, bool) {
		if kind != "line_comment" || !strings.HasPrefix(text, "///") || strings.HasPrefix(text, "////") {
			return "", false
		}
		return strings.TrimSpace(strings.TrimPrefix(text, "///")), true
	},
	skip: func(kind string) bool { return kind == "attribute_item" },
}

// extractRust walks a parsed Rust source_file. The module doc is the
// leading `//!` run; item docs are `///` runs. Only items with exactly
// `pub` visibility are exported.
func extractRust(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{PackageDoc: rustInnerDocsAtHead(source, root)}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() == "use_declaration" {
			if text := fieldText(source, c, "argument"); text != "" {
				d.Imports = append(d.Imports, text)
			}
			continue
		}
		if !rustIsPublic(source, c) {
			continue
		}
		nm := fieldText(source, c, "name")
		if nm == "" {
			continue
		}
		switch c.Kind() {
		case "function_item":
			d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{
				Name:      nm,
				Signature: signatureBefore(source, c, c.ChildByFieldName("body")),
				Doc:       collectDocAbove(source, c, rustDoc),
			})
		case "struct_item":
			rustAddType(source, c, nm, d, "field_declaration")
		case "enum_item":
			rustAddType(source, c, nm, d, "enum_variant")
		case "trait_item":
			rustAddType(source, c, nm, d, "function_signature_item", "function_item")
		case "type_item":
			rustAddType(source, c, nm, d)
		case "const_item":
			d.Consts = append(d.Consts, nm)
		}
	}
	return d
}

func rustInnerDocsAtHead(source []byte, root *sitter.Node) string {
	var lines []string
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		text := nodeText(source, c)
		if c.Kind() != "line_comment" || !strings.HasPrefix(text, "//!") {
			break
		}
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(text, "//!")))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// rustIsPublic reports whether decl has exactly `pub` visibility
// (pub(crate), pub(super) and pub(in ..) are not part of the public API).
func rustIsPublic(source []byte, decl *sitter.Node) bool {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		if ch := decl.NamedChild(i); ch.Kind() == "visibility_modifier" {
			return nodeText(source, ch) == "pub"
		}
	}
	return false
}

// rustAddType records a type item, listing body children of memberKinds
// as its fields.
func rustAddType(source []byte, decl *sitter.Node, nm string, d *StructuralData, memberKinds ...string) {
	var fields []string
	if body := decl.ChildByFieldName("body"); body != nil {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			m := body.NamedChild(i)
			for _, k := range memberKinds {
				if m.Kind() == k {
					fields = append(fields, collapse(nodeText(source, m)))
				}
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: collectDocAbove(source, decl, rustDoc), Fields: fields})
}
