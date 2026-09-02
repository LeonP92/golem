package wiki

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

type Doc struct {
	Path    string
	Content string
	Hash    string
}

// LoadAll reads every .md file under wikiDir, recursively.
func LoadAll(wikiDir string) ([]Doc, error) {
	var docs []Doc
	err := filepath.WalkDir(wikiDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		docs = append(docs, Doc{
			Path:    path,
			Content: string(content),
			Hash:    Hash(string(content)),
		})
		return nil
	})
	return docs, err
}

func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// IsStale reports whether doc's current content hash differs from a
// previously stored hash — the trigger for rebuilding the dedup index
// (spec: Error Handling — wiki/index staleness).
func IsStale(doc Doc, storedHash string) bool {
	return Hash(doc.Content) != storedHash
}
