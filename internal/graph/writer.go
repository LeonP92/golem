package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func WriteModule(wikiDir string, g ModuleGraph) error {
	dir := filepath.Join(wikiDir, "graph", "modules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n%s\n", g.Module, g.Summary)
	if len(g.ExportFns) > 0 {
		b.WriteString("\n## Functions\n\n")
		for _, fn := range g.ExportFns {
			fmt.Fprintf(&b, "- %s\n", fn)
		}
	}
	if len(g.ExportTypes) > 0 {
		b.WriteString("\n## Types\n\n")
		for _, t := range g.ExportTypes {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}
	if len(g.Imports) > 0 {
		fmt.Fprintf(&b, "\n## Imports\n\n%s\n", strings.Join(g.Imports, ", "))
	}
	return os.WriteFile(filepath.Join(dir, slug(g.Module)+".md"), []byte(b.String()), 0o644)
}

func AppendSymbols(wikiDir string, g ModuleGraph) error {
	if len(g.ExportFns) == 0 {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(wikiDir, "graph", "symbols.md"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, fn := range g.ExportFns {
		fmt.Fprintf(f, "- `%s` — %s\n", g.Module, fn)
	}
	return nil
}

func AppendTypes(wikiDir string, g ModuleGraph) error {
	if len(g.ExportTypes) == 0 {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(wikiDir, "graph", "types.md"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, t := range g.ExportTypes {
		fmt.Fprintf(f, "- `%s` — %s\n", g.Module, t)
	}
	return nil
}

func WriteIndex(wikiDir string, graphs []ModuleGraph) error {
	if err := os.MkdirAll(filepath.Join(wikiDir, "graph"), 0o755); err != nil {
		return err
	}
	bySubsystem := map[string][]ModuleGraph{}
	for _, g := range graphs {
		sub := g.Subsystem
		if sub == "" {
			sub = "misc"
		}
		bySubsystem[sub] = append(bySubsystem[sub], g)
	}
	subsystems := make([]string, 0, len(bySubsystem))
	for s := range bySubsystem {
		subsystems = append(subsystems, s)
	}
	sort.Strings(subsystems)

	var b strings.Builder
	b.WriteString("# Codebase Index\n\n")
	for _, sub := range subsystems {
		fmt.Fprintf(&b, "## %s\n\n", sub)
		for _, g := range bySubsystem[sub] {
			fmt.Fprintf(&b, "### [%s](modules/%s.md)\n\n%s\n\n", g.Module, slug(g.Module), g.Summary)
		}
	}
	return os.WriteFile(filepath.Join(wikiDir, "graph", "index.md"), []byte(b.String()), 0o644)
}

func ResetAggregates(wikiDir string) error {
	if err := os.MkdirAll(filepath.Join(wikiDir, "graph"), 0o755); err != nil {
		return err
	}
	for _, name := range []string{"symbols.md", "types.md"} {
		if err := os.WriteFile(filepath.Join(wikiDir, "graph", name), nil, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func slug(path string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ".", "_")
	return r.Replace(path)
}
