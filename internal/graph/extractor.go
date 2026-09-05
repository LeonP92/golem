package graph

import (
	"embed"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	golang "github.com/tree-sitter/tree-sitter-go/bindings/go"
	java "github.com/tree-sitter/tree-sitter-java/bindings/go"
	javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	rust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries/*.scm
var queryFS embed.FS

// StructuralData holds the tree-sitter-extracted structural information for a
// source file.
type StructuralData struct {
	Imports     []string
	ExportFns   []string
	ExportTypes []string
}

var extToLang = map[string]string{
	".go":   "golang",
	".py":   "python",
	".ts":   "typescript",
	".tsx":  "typescript",
	".js":   "javascript",
	".jsx":  "javascript",
	".mjs":  "javascript",
	".rs":   "rust",
	".java": "java",
}

// LangForExt returns the language name for a file extension, or "" if unknown.
func LangForExt(ext string) string {
	return extToLang[ext]
}

func tsLanguage(name string) *sitter.Language {
	switch name {
	case "golang":
		return sitter.NewLanguage(golang.Language())
	case "python":
		return sitter.NewLanguage(python.Language())
	case "typescript":
		return sitter.NewLanguage(typescript.LanguageTypescript())
	case "javascript":
		return sitter.NewLanguage(javascript.Language())
	case "rust":
		return sitter.NewLanguage(rust.Language())
	case "java":
		return sitter.NewLanguage(java.Language())
	}
	return nil
}

// Extract runs tree-sitter queries against source to extract imports and
// exported symbols. Returns empty StructuralData (not an error) for unknown
// languages.
func Extract(source []byte, langName string) (*StructuralData, error) {
	lang := tsLanguage(langName)
	if lang == nil {
		return &StructuralData{}, nil
	}

	queryBytes, err := queryFS.ReadFile("queries/" + langName + ".scm")
	if err != nil {
		return &StructuralData{}, nil
	}

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		return nil, err
	}
	tree := parser.Parse(source, nil)
	defer tree.Close()

	q, qErr := sitter.NewQuery(lang, string(queryBytes))
	if qErr != nil {
		return nil, qErr
	}
	defer q.Close()

	cursor := sitter.NewQueryCursor()
	defer cursor.Close()

	root := tree.RootNode()
	matches := cursor.Matches(q, root, source)

	d := &StructuralData{}
	seen := make(map[string]bool) // deduplicate within module

	captureNames := q.CaptureNames()

	for match := matches.Next(); match != nil; match = matches.Next() {
		// Filter matches that have text predicates (#match?, #not-match?, etc.)
		if !match.SatisfiesTextPredicate(q, nil, nil, source) {
			continue
		}
		for _, cap := range match.Captures {
			name := captureNames[cap.Index]
			node := cap.Node
			text := strings.TrimSpace(string(source[node.StartByte():node.EndByte()]))
			// Strip surrounding quotes for import paths (e.g. Go's "fmt")
			text = strings.Trim(text, `"`)
			text = strings.TrimSpace(text)
			if text == "" || seen[name+":"+text] {
				continue
			}
			seen[name+":"+text] = true
			switch name {
			case "import":
				d.Imports = append(d.Imports, text)
			case "export_fn":
				d.ExportFns = append(d.ExportFns, text)
			case "export_type":
				d.ExportTypes = append(d.ExportTypes, text)
			}
		}
	}
	return d, nil
}
