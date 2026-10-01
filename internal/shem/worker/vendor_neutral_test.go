package worker

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The shem runs whichever adapter it is configured with, so no string literal
// in this package may name a vendor. Comments and test data are exempt.
func TestNoVendorNameInShemCode(t *testing.T) {
	// A Go string literal on a line that is not a comment.
	literal := regexp.MustCompile(`"[^"]*(?i:claude|anthropic|codex|gemini)[^"]*"`)

	root := ".."
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // paths come from the walk
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			if m := literal.FindString(line); m != "" {
				t.Errorf("%s:%d names a vendor in code: %s", path, i+1, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/shem: %v", err)
	}
}
