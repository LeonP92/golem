package graph

import "strings"

type ModuleGraph struct {
	Module     string
	Summary    string
	ExportFns  []string
	ExportTypes []string
	Imports    []string
	Calls      []string
	Subsystem  string
}

func Parse(output string) ModuleGraph {
	var g ModuleGraph
	for _, line := range strings.Split(output, "\n") {
		tag, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch tag {
		case "GRAPH_MODULE":
			g.Module = val
		case "GRAPH_SUMMARY":
			g.Summary = val
		case "GRAPH_EXPORT_FN":
			g.ExportFns = append(g.ExportFns, val)
		case "GRAPH_EXPORT_TYPE":
			g.ExportTypes = append(g.ExportTypes, val)
		case "GRAPH_IMPORTS":
			g.Imports = splitCSV(val)
		case "GRAPH_CALLS":
			g.Calls = splitCSV(val)
		case "GRAPH_SUBSYSTEM":
			g.Subsystem = val
		}
	}
	return g
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
