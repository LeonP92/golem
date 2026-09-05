package graph

import "strings"

type ModuleGraph struct {
	Module      string
	Summary     string
	ExportFns   []string
	ExportTypes []string
	Imports     []string
	Subsystem   string
}

// Parse extracts Summary and Subsystem from LLM output. Structural fields
// (Imports, ExportFns, ExportTypes) must be set by the caller from tree-sitter
// results.
func Parse(output string) ModuleGraph {
	var g ModuleGraph
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "GRAPH_MODULE:"):
			g.Module = strings.TrimPrefix(line, "GRAPH_MODULE:")
		case strings.HasPrefix(line, "GRAPH_SUMMARY:"):
			g.Summary = strings.TrimPrefix(line, "GRAPH_SUMMARY:")
		case strings.HasPrefix(line, "GRAPH_SUBSYSTEM:"):
			g.Subsystem = strings.TrimPrefix(line, "GRAPH_SUBSYSTEM:")
		}
	}
	return g
}
