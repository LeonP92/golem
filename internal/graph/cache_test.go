package graph

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestModuleCacheKey_deterministic(t *testing.T) {
	hashes := map[string]string{
		"b.go": "bbb",
		"a.go": "aaa",
	}
	k1 := ModuleCacheKey("internal/foo", hashes)
	k2 := ModuleCacheKey("internal/foo", hashes)
	if k1 != k2 {
		t.Fatalf("keys differ: %q vs %q", k1, k2)
	}
}

func TestModuleCacheKey_changesWithContent(t *testing.T) {
	h1 := map[string]string{"a.go": "aaa"}
	h2 := map[string]string{"a.go": "bbb"}
	if ModuleCacheKey("p", h1) == ModuleCacheKey("p", h2) {
		t.Fatal("keys should differ when content differs")
	}
}

func TestLLMCache_roundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "llm_cache.json")

	c, err := LoadLLMCache(path)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	c.Set("key1", "summary text", "auth")
	if err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	c2, err := LoadLLMCache(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	sum, sub, ok := c2.Get("key1")
	if !ok {
		t.Fatal("key1 not found after reload")
	}
	if sum != "summary text" || sub != "auth" {
		t.Fatalf("got sum=%q sub=%q", sum, sub)
	}
}

func TestLLMCache_missingFileReturnsEmpty(t *testing.T) {
	c, err := LoadLLMCache("/nonexistent/path/cache.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	_, _, ok := c.Get("anything")
	if ok {
		t.Fatal("expected miss on empty cache")
	}
}

func TestLLMCache_concurrentGetSet(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadLLMCache(filepath.Join(dir, "cache.json"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("k%d", n)
			c.Set(key, "summary", "sub")
			c.Get(key)
		}(i)
	}
	wg.Wait()
}
