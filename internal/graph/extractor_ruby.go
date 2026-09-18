package graph

import (
	"regexp"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// rubyMagicComment matches shebangs and interpreter magic comments, which
// are never documentation.
var rubyMagicComment = regexp.MustCompile(`^#!|^#\s*(frozen_string_literal|encoding|coding|warn_indent|shareable_constant_value)\s*:|^#.*-\*-.*-\*-`)

var rubyDoc = docStyle{line: func(kind, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if kind != "comment" || rubyMagicComment.MatchString(text) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(text, "#")), true
}}

// extractRuby walks a parsed Ruby program. Top-level methods, classes,
// modules, and CONSTANT assignments are exports, as are public methods in
// class/module bodies (recorded as "Class#method" / "Class.method").
// `require`/`require_relative` calls become imports.
func extractRuby(source []byte, root *sitter.Node) *StructuralData {
	d := &StructuralData{PackageDoc: rubyLeadingComments(source, root)}
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		switch c.Kind() {
		case "call":
			rubyMaybeRequire(source, c, d)
		case "assignment":
			if left := c.ChildByFieldName("left"); left != nil && left.Kind() == "constant" {
				d.Consts = append(d.Consts, nodeText(source, left))
			}
		case "method":
			rubyAddMethod(source, c, "", d)
		case "class", "module":
			rubyExtractType(source, c, "", d)
		}
	}
	return d
}

// rubyLeadingComments returns the first run of line-adjacent # comments
// at the top of the file, skipping shebang and magic comments.
func rubyLeadingComments(source []byte, root *sitter.Node) string {
	var lines []string
	prevRow := -1
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "comment" {
			break
		}
		doc, ok := rubyDoc.line(c.Kind(), nodeText(source, c))
		if !ok {
			if len(lines) > 0 {
				break
			}
			continue
		}
		if row := int(c.StartPosition().Row); prevRow >= 0 && row > prevRow+1 {
			break
		}
		lines = append(lines, doc)
		prevRow = int(c.EndPosition().Row)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func rubyMaybeRequire(source []byte, call *sitter.Node, d *StructuralData) {
	name := fieldText(source, call, "method")
	if name != "require" && name != "require_relative" {
		return
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return
	}
	if text := strings.Trim(collapse(nodeText(source, args.NamedChild(0))), `"'`); text != "" {
		d.Imports = append(d.Imports, text)
	}
}

// rubyAddMethod records a method as "def name(params)"; inside a class or
// module the name is qualified as "Owner#name" (instance) or "Owner.name"
// (singleton).
func rubyAddMethod(source []byte, decl *sitter.Node, owner string, d *StructuralData) {
	nm := fieldText(source, decl, "name")
	if nm == "" {
		return
	}
	sig := "def "
	qualified := nm
	if decl.Kind() == "singleton_method" {
		sig += fieldText(source, decl, "object") + "."
		if owner != "" {
			qualified = owner + "." + nm
		}
	} else if owner != "" {
		qualified = owner + "#" + nm
	}
	sig += nm + fieldText(source, decl, "parameters")
	d.ExportedFuncs = append(d.ExportedFuncs, ExportedFunc{Name: qualified, Signature: sig, Doc: rubyDocAbove(source, decl)})
}

// rubyExtractType records a class/module (attr_* calls as fields) and its
// public methods, recursing into nested classes/modules. A bare
// `private`/`protected` line hides subsequent instance methods until
// `public`; singleton methods are unaffected.
func rubyExtractType(source []byte, decl *sitter.Node, outer string, d *StructuralData) {
	nm := fieldText(source, decl, "name")
	if nm == "" {
		return
	}
	if outer != "" {
		nm = outer + "::" + nm
	}
	var fields []string
	var nested []*sitter.Node
	public := true
	if body := decl.ChildByFieldName("body"); body != nil {
		for i := uint(0); i < body.NamedChildCount(); i++ {
			ch := body.NamedChild(i)
			switch ch.Kind() {
			case "identifier":
				switch nodeText(source, ch) {
				case "private", "protected":
					public = false
				case "public":
					public = true
				}
			case "call":
				if strings.HasPrefix(fieldText(source, ch, "method"), "attr_") {
					fields = append(fields, collapse(nodeText(source, ch)))
				}
			case "method":
				if public {
					rubyAddMethod(source, ch, nm, d)
				}
			case "singleton_method":
				rubyAddMethod(source, ch, nm, d)
			case "class", "module":
				nested = append(nested, ch)
			}
		}
	}
	d.ExportedTypes = append(d.ExportedTypes, ExportedType{Name: nm, Doc: rubyDocAbove(source, decl), Fields: fields})
	for _, n := range nested {
		rubyExtractType(source, n, nm, d)
	}
}

// rubyDocAbove returns the # comment run above decl. The grammar attaches
// comments preceding the first statement of a body to the enclosing
// class/module rather than to the body, so look there too.
func rubyDocAbove(source []byte, decl *sitter.Node) string {
	if decl.PrevSibling() == nil {
		if p := decl.Parent(); p != nil && p.Kind() == "body_statement" {
			return collectDocAbove(source, p, rubyDoc)
		}
	}
	return collectDocAbove(source, decl, rubyDoc)
}
