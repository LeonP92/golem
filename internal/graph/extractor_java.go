package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// extractJava walks a parsed Java compilation unit. Java has no "module
// doc" concept; PackageDoc is set from the block_comment immediately
// preceding the first top-level class/interface, which is the closest
// analogue in idiomatic Java sources.
func extractJava(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	cursor := root.Walk()
	defer cursor.Close()
	children := root.NamedChildren(cursor)

	packageDocSet := false
	for i := range children {
		c := &children[i]
		switch c.Kind() {
		case "import_declaration":
			extractJavaImport(source, c, d)
		case "class_declaration":
			if !packageDocSet {
				d.PackageDoc = javaDocAbove(source, c)
				packageDocSet = true
			}
			extractJavaClass(source, c, d)
		case "interface_declaration":
			if !packageDocSet {
				d.PackageDoc = javaDocAbove(source, c)
				packageDocSet = true
			}
			extractJavaInterface(source, c, d)
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

func extractJavaImport(source []byte, decl *sitter.Node, d *StructuralData) {
	// The named child is the scoped_identifier we want verbatim.
	if decl.NamedChildCount() == 0 {
		return
	}
	child := decl.NamedChild(0)
	if child == nil {
		return
	}
	text := strings.TrimSpace(string(source[child.StartByte():child.EndByte()]))
	if text != "" {
		d.Imports = append(d.Imports, text)
	}
}

func extractJavaClass(source []byte, decl *sitter.Node, d *StructuralData) {
	if !javaHasModifier(source, decl, "public") {
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
			switch ch.Kind() {
			case "field_declaration":
				if !javaHasModifier(source, ch, "public") {
					continue
				}
				text := strings.TrimSpace(string(source[ch.StartByte():ch.EndByte()]))
				text = strings.Join(strings.Fields(text), " ")
				fields = append(fields, text)
			case "method_declaration":
				if !javaHasModifier(source, ch, "public") {
					continue
				}
				extractJavaMethod(source, ch, d)
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: javaDocAbove(source, decl), Fields: fields})
}

func extractJavaInterface(source []byte, decl *sitter.Node, d *StructuralData) {
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
			if ch.Kind() == "method_declaration" {
				text := strings.TrimSpace(string(source[ch.StartByte():ch.EndByte()]))
				text = strings.Join(strings.Fields(text), " ")
				fields = append(fields, text)
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: javaDocAbove(source, decl), Fields: fields})
}

func extractJavaMethod(source []byte, decl *sitter.Node, d *StructuralData) {
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
	doc := javaDocAbove(source, decl)
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: doc})
}

func javaHasModifier(source []byte, decl *sitter.Node, want string) bool {
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		ch := decl.NamedChild(i)
		if ch == nil || ch.Kind() != "modifiers" {
			continue
		}
		text := string(source[ch.StartByte():ch.EndByte()])
		for _, tok := range strings.Fields(text) {
			if tok == want {
				return true
			}
		}
	}
	return false
}

// javaDocAbove finds a block_comment sibling immediately preceding node
// (line-adjacent) and returns its stripped text, or "".
func javaDocAbove(source []byte, node *sitter.Node) string {
	prev := node.PrevSibling()
	if prev == nil || prev.Kind() != "block_comment" {
		return ""
	}
	if prev.EndPosition().Row+1 < node.StartPosition().Row {
		return ""
	}
	return stripJSDoc(string(source[prev.StartByte():prev.EndByte()]))
}
