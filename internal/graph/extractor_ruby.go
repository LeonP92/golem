package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// extractRuby walks a parsed Ruby program. Ruby has no explicit
// visibility keyword at top level (methods are public by default), so
// all defined methods, classes, modules, and top-level CONSTANT
// assignments are recorded as exports. `require`/`require_relative`
// calls become imports.
func extractRuby(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{}
	cursor := root.Walk()
	defer cursor.Close()
	children := root.NamedChildren(cursor)

	// PackageDoc: leading run of # comments before any code.
	d.PackageDoc = rubyLeadingComments(source, children)

	for i := range children {
		c := &children[i]
		switch c.Kind() {
		case "call":
			rubyMaybeRequire(source, c, d)
		case "assignment":
			rubyMaybeConst(source, c, d)
		case "method":
			rubyExtractMethod(source, c, d)
		case "class":
			rubyExtractClass(source, c, d)
		case "module":
			rubyExtractModule(source, c, d)
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

func rubyLeadingComments(source []byte, children []sitter.Node) string {
	var lines []string
	prevRow := -1
	for i := range children {
		c := &children[i]
		if c.Kind() != "comment" {
			break
		}
		startRow := int(c.StartPosition().Row)
		if prevRow >= 0 && startRow > prevRow+1 {
			break
		}
		text := strings.TrimSpace(string(source[c.StartByte():c.EndByte()]))
		text = strings.TrimPrefix(text, "#")
		lines = append(lines, strings.TrimSpace(text))
		prevRow = int(c.EndPosition().Row)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// rubyCommentAbove returns the # comment run immediately preceding node.
func rubyCommentAbove(source []byte, node *sitter.Node) string {
	var lines []string
	prev := node.PrevSibling()
	lastRow := node.StartPosition().Row
	for prev != nil && prev.Kind() == "comment" {
		if prev.EndPosition().Row+1 < lastRow {
			break
		}
		text := strings.TrimSpace(string(source[prev.StartByte():prev.EndByte()]))
		text = strings.TrimPrefix(text, "#")
		lines = append([]string{strings.TrimSpace(text)}, lines...)
		lastRow = prev.StartPosition().Row
		prev = prev.PrevSibling()
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func rubyMaybeRequire(source []byte, call *sitter.Node, d *StructuralData) {
	method := call.ChildByFieldName("method")
	if method == nil {
		return
	}
	name := string(source[method.StartByte():method.EndByte()])
	if name != "require" && name != "require_relative" {
		return
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return
	}
	arg := args.NamedChild(0)
	if arg == nil {
		return
	}
	text := strings.TrimSpace(string(source[arg.StartByte():arg.EndByte()]))
	text = strings.Trim(text, `"'`)
	if text != "" {
		d.Imports = append(d.Imports, text)
	}
}

func rubyMaybeConst(source []byte, decl *sitter.Node, d *StructuralData) {
	left := decl.ChildByFieldName("left")
	if left == nil || left.Kind() != "constant" {
		return
	}
	nm := string(source[left.StartByte():left.EndByte()])
	if nm != "" {
		d.Consts = append(d.Consts, nm)
	}
}

func rubyExtractMethod(source []byte, decl *sitter.Node, d *StructuralData) {
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	// Signature = "def name(params)"
	params := decl.ChildByFieldName("parameters")
	sig := "def " + nm
	if params != nil {
		sig += string(source[params.StartByte():params.EndByte()])
	}
	sig = strings.Join(strings.Fields(sig), " ")
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: nm, Signature: sig, Doc: rubyCommentAbove(source, decl)})
}

func rubyExtractClass(source []byte, decl *sitter.Node, d *StructuralData) {
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
			// attr_accessor/attr_reader/attr_writer calls list fields.
			if ch.Kind() == "call" {
				m := ch.ChildByFieldName("method")
				if m == nil {
					continue
				}
				mn := string(source[m.StartByte():m.EndByte()])
				if strings.HasPrefix(mn, "attr_") {
					text := strings.TrimSpace(string(source[ch.StartByte():ch.EndByte()]))
					text = strings.Join(strings.Fields(text), " ")
					fields = append(fields, text)
				}
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: rubyCommentAbove(source, decl), Fields: fields})
}

func rubyExtractModule(source []byte, decl *sitter.Node, d *StructuralData) {
	name := decl.ChildByFieldName("name")
	if name == nil {
		return
	}
	nm := string(source[name.StartByte():name.EndByte()])
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: rubyCommentAbove(source, decl)})
}
