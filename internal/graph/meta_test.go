package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyJSONLocationFieldsOptional(t *testing.T) {
	index := t.TempDir()
	dir := filepath.Join(index, "graph-modules")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"name-only.json": `{"module":"old","export_fns":["Legacy"],"export_types":["Config"]}`,
		"path-only.json": `{"module":"path","exported_funcs":[{"name":"Run","file":"pkg/a.go"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	graphs, err := LoadAllModuleGraphs(index)
	if err != nil || len(graphs) != 2 {
		t.Fatalf("graphs=%+v err=%v", graphs, err)
	}
	wiki := t.TempDir()
	ResetAggregates(wiki)
	for _, g := range graphs {
		if err := WriteModule(wiki, g); err != nil {
			t.Fatal(err)
		}
		if err := AppendSymbols(wiki, g); err != nil {
			t.Fatal(err)
		}
		if err := AppendTypes(wiki, g); err != nil {
			t.Fatal(err)
		}
	}
}
