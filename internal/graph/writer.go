package graph

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteModule renders a single module wiki page from tree-sitter data.
// The page format is deterministic — inputs of the same content produce
// byte-identical output, no LLM in the loop. Sections are omitted when
// empty. The "Part of subsystem" line links to the shared index page.
func WriteModule(wikiDir string, g ModuleGraph) error {
	dir := filepath.Join(wikiDir, "graph", "modules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", g.Module)

	summary := g.Summary
	if summary == "" {
		summary = g.PackageDoc
	}
	if summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}

	sub := g.Subsystem
	if sub == "" {
		sub = "misc"
	}
	fmt.Fprintf(&b, "Part of subsystem: [%s](../index.md#%s)\n", sub, sub)

	if len(g.ExportedFuncs) > 0 {
		b.WriteString("\n## Functions\n\n")
		for _, fn := range g.ExportedFuncs {
			if fn.Signature != "" {
				fmt.Fprintf(&b, "### `%s`\n\n```\n%s\n```\n", fn.Name, fn.Signature)
			} else {
				fmt.Fprintf(&b, "### `%s`\n\n", fn.Name)
			}
			if fn.Doc != "" {
				b.WriteString(fn.Doc)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		}
	} else if len(g.ExportFns) > 0 {
		// Fallback for languages without extended extraction.
		b.WriteString("\n## Functions\n\n")
		for _, fn := range g.ExportFns {
			fmt.Fprintf(&b, "- %s\n", fn)
		}
	}

	if len(g.ExportedTypes) > 0 {
		b.WriteString("\n## Types\n\n")
		for _, t := range g.ExportedTypes {
			fmt.Fprintf(&b, "### `%s`\n\n", t.Name)
			if t.Doc != "" {
				b.WriteString(t.Doc)
				b.WriteString("\n\n")
			}
			if len(t.Fields) > 0 {
				b.WriteString("```\n")
				for _, f := range t.Fields {
					b.WriteString(f)
					b.WriteString("\n")
				}
				b.WriteString("```\n")
			}
			b.WriteString("\n")
		}
	} else if len(g.ExportTypes) > 0 {
		b.WriteString("\n## Types\n\n")
		for _, t := range g.ExportTypes {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}

	if len(g.Consts) > 0 {
		b.WriteString("\n## Constants\n\n")
		for _, c := range g.Consts {
			fmt.Fprintf(&b, "- `%s`\n", c)
		}
	}

	if len(g.Imports) > 0 {
		b.WriteString("\n## Imports\n\n")
		for _, imp := range g.Imports {
			fmt.Fprintf(&b, "- %s\n", imp)
		}
	}

	return atomicWriteFile(filepath.Join(dir, Slug(g.Module)+".md"), []byte(b.String()), 0o644)
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

// SubsystemNarratives maps a subsystem name to the cluster narrative
// rendered from a single LLM invocation per cluster (see step 12). Passed
// through WriteIndex so the deterministic writer stays pure.
type SubsystemNarratives map[string]string

// WriteIndex writes the top-level codebase index, grouping modules by
// subsystem. The optional narratives map is rendered under each subsystem
// heading; when a subsystem has no narrative, the section is emitted with
// its module list only.
func WriteIndex(wikiDir string, graphs []ModuleGraph, narratives ...SubsystemNarratives) error {
	if err := os.MkdirAll(filepath.Join(wikiDir, "graph"), 0o755); err != nil {
		return err
	}
	var narr SubsystemNarratives
	if len(narratives) > 0 {
		narr = narratives[0]
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
		if n, ok := narr[sub]; ok && n != "" {
			b.WriteString(n)
			b.WriteString("\n\n")
		}
		mods := bySubsystem[sub]
		sort.Slice(mods, func(i, j int) bool { return mods[i].Module < mods[j].Module })
		for _, g := range mods {
			summary := g.Summary
			if summary == "" {
				summary = g.PackageDoc
			}
			fmt.Fprintf(&b, "### [%s](modules/%s.md)\n\n%s\n\n", g.Module, Slug(g.Module), summary)
		}
	}
	return atomicWriteFile(filepath.Join(wikiDir, "graph", "index.md"), []byte(b.String()), 0o644)
}

func ResetAggregates(wikiDir string) error {
	if err := os.MkdirAll(filepath.Join(wikiDir, "graph"), 0o755); err != nil {
		return err
	}
	for _, name := range []string{"symbols.md", "types.md"} {
		if err := atomicWriteFile(filepath.Join(wikiDir, "graph", name), nil, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func Slug(path string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ".", "_")
	return r.Replace(path)
}
