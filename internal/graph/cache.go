package graph

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

type cacheEntry struct {
	Summary   string `json:"summary"`
	Subsystem string `json:"subsystem"`
}

// LLMCache is an in-memory LLM response cache backed by a JSON file.
type LLMCache struct {
	mu      sync.RWMutex
	path    string
	entries map[string]cacheEntry
}

// LoadLLMCache loads the cache from path. Returns an empty cache if the file
// does not exist.
func LoadLLMCache(path string) (*LLMCache, error) {
	c := &LLMCache{path: path, entries: make(map[string]cacheEntry)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	return c, json.Unmarshal(data, &c.entries)
}

// Get returns the cached summary and subsystem for a module key.
func (c *LLMCache) Get(key string) (summary, subsystem string, ok bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	return e.Summary, e.Subsystem, ok
}

// Set stores a summary and subsystem for a module key.
func (c *LLMCache) Set(key, summary, subsystem string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{Summary: summary, Subsystem: subsystem}
}

// Save persists the cache atomically.
func (c *LLMCache) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, err := json.MarshalIndent(c.entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(c.path, data, 0o644)
}

// ModuleCacheKey returns a deterministic cache key for a module.
// It is derived from the module path and the sorted concatenation of file
// content hashes, so any file change produces a different key.
func ModuleCacheKey(modulePath string, fileHashes map[string]string) string {
	keys := make([]string, 0, len(fileHashes))
	for k := range fileHashes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(modulePath)
	sb.WriteByte(':')
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(fileHashes[k])
		sb.WriteByte(';')
	}
	h := sha256.Sum256([]byte(sb.String()))
	return fmt.Sprintf("%x", h[:8])
}
