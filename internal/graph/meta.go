package graph

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type ModuleMeta struct {
	Commit     string            `json:"commit"`
	FileHashes map[string]string `json:"file_hashes"`
}

type Meta struct {
	BaseCommit string                `json:"base_commit"`
	Modules    map[string]ModuleMeta `json:"modules"`
}

type Edges struct {
	Imports map[string][]string `json:"imports"`
	Calls   map[string][]string `json:"calls"`
}

func LoadMeta(indexDir string) (*Meta, error) {
	data, err := os.ReadFile(filepath.Join(indexDir, "graph-meta.json"))
	if os.IsNotExist(err) {
		return &Meta{Modules: map[string]ModuleMeta{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var m Meta
	return &m, json.Unmarshal(data, &m)
}

func SaveMeta(indexDir string, m *Meta) error {
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(indexDir, "graph-meta.json"), data, 0o644)
}

func SaveEdges(indexDir string, graphs []ModuleGraph) error {
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		return err
	}
	e := Edges{
		Imports: map[string][]string{},
		Calls:   map[string][]string{},
	}
	for _, g := range graphs {
		if len(g.Imports) > 0 {
			e.Imports[g.Module] = g.Imports
		}
		if len(g.Calls) > 0 {
			e.Calls[g.Module] = g.Calls
		}
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(indexDir, "graph-edges.json"), data, 0o644)
}

func SaveModuleGraph(indexDir string, g ModuleGraph) error {
	dir := filepath.Join(indexDir, "graph-modules")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(g)
	if err != nil {
		return err
	}
	name := strings.NewReplacer("/", "_", "\\", "_", ".", "_").Replace(g.Module) + ".json"
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

func LoadAllModuleGraphs(indexDir string) ([]ModuleGraph, error) {
	dir := filepath.Join(indexDir, "graph-modules")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var graphs []ModuleGraph
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var g ModuleGraph
		if err := json.Unmarshal(data, &g); err != nil {
			return nil, err
		}
		graphs = append(graphs, g)
	}
	return graphs, nil
}

func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func ModuleHashes(repoRoot string, files []string) (map[string]string, error) {
	hashes := make(map[string]string, len(files))
	for _, f := range files {
		h, err := FileHash(filepath.Join(repoRoot, f))
		if err != nil {
			return nil, err
		}
		hashes[f] = h
	}
	return hashes, nil
}
