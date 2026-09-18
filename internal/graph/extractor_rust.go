package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// extractRust walks a parsed Rust source_file. Rust exposes doc comments
// as line_comment nodes whose kind starts with either `outer_doc_comment_marker`
// (///, before an item) or `inner_doc_comment_marker` (//!, module-level).
// Both are collected as consecutive-line runs.
func extractRust(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	cursor := root.Walk()
	defer cursor.Close()
	children := root.NamedChildren(cursor)

	// Module doc: leading //! comment run.
	d.PackageDoc = rustInnerDocsAtHead(source, children)

	for i := range children {
		c := &children[i]
		switch c.Kind() {
		case "use_declaration":
			extractRustUse(source, c, d)
		case "function_item":
			extractRustFunc(source, c, d)
		case "struct_item":
			extractRustStruct(source, c, d)
		case "trait_item":
			extractRustTrait(source, c, d)
		case "const_item":
			extractRustConst(source, c, d)
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

func rustInnerDocsAtHead(source []byte, children []sitter.Node) string {
	var lines []string
	for i := range children {
		c := &children[i]
		if c.Kind() != "line_comment" {
			break
		}
		text := string(source[c.StartByte():c.EndByte()])
		if !strings.HasPrefix(text, "//!") {
			break
		}
		lines = append(lines, strings.TrimSpace(strings.TrimPrefix(text, "//!")))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// rustOuterDocsAbove collects /// comment lines immediately preceding node.
func rustOuterDocsAbove(source []byte, node *sitter.Node) string {
	var lines []string
	prev := node.PrevSibling()
	lastRow := node.StartPosition().Row
	for prev != nil && prev.Kind() == "line_comment" {
		text := string(source[prev.StartByte():prev.EndByte()])
		if !strings.HasPrefix(text, "///") || strings.HasPrefix(text, "////") {
			break
		}
		if prev.EndPosition().Row+1 < lastRow {
			break
		}
		lines = append([]string{strings.TrimSpace(strings.TrimPrefix(text, "///"))}, lines...)
		lastRow = prev.StartPosition().Row
		prev = prev.PrevSibling()
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func rustIsPublic(decl *sitter.Node) bool {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		ch := decl.NamedChild(i)
		if ch != nil && ch.Kind() == "visibility_modifier" {
			return true
		}
	}
	return false
}

func extractRustUse(source []byte, decl *sitter.Node, d *StructuralData) {
	arg := decl.ChildByFieldName("argument")
	if arg == nil {
		return
	}
	text := strings.TrimSpace(string(source[arg.StartByte():arg.EndByte()]))
	if text != "" {
		d.Imports = append(d.Imports, text)
	}
}

func extractRustFunc(source []byte, decl *sitter.Node, d *StructuralData) {
	if !rustIsPublic(decl) {
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
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: rustOuterDocsAbove(source, decl)})
}

func extractRustStruct(source []byte, decl *sitter.Node, d *StructuralData) {
	if !rustIsPublic(decl) {
		return
	}
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	body := decl.ChildByFieldName("body")
	var fields []string
	if body != nil && body.Kind() == "field_declaration_list" {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			fd := body.NamedChild(i)
			if fd == nil || fd.Kind() != "field_declaration" {
				continue
			}
			text := strings.TrimSpace(string(source[fd.StartByte():fd.EndByte()]))
			text = strings.Join(strings.Fields(text), " ")
			fields = append(fields, text)
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: rustOuterDocsAbove(source, decl), Fields: fields})
}

func extractRustTrait(source []byte, decl *sitter.Node, d *StructuralData) {
	if !rustIsPublic(decl) {
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
			m := body.NamedChild(i)
			if m == nil {
				continue
			}
			if m.Kind() == "function_signature_item" || m.Kind() == "function_item" {
				text := strings.TrimSpace(string(source[m.StartByte():m.EndByte()]))
				text = strings.Join(strings.Fields(text), " ")
				fields = append(fields, text)
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: rustOuterDocsAbove(source, decl), Fields: fields})
}

func extractRustConst(source []byte, decl *sitter.Node, d *StructuralData) {
	if !rustIsPublic(decl) {
		return
	}
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	d.Consts = append(d.Consts, nm)
}
