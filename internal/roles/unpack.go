package roles

import (
	"os"
	"path/filepath"
)

// Unpack writes the embedded default role files into
// <golemDir>/roles/<name>.md, skipping any file that already exists.
// Once unpacked, role files are owned by the target repo — golem init
// never overwrites a customized file on re-run (spec: Distribution &
// Ownership, decision A).
func Unpack(golemDir string) ([]string, error) {
	rolesDir := filepath.Join(golemDir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		return nil, err
	}

	var written []string
	for _, name := range RoleNames {
		dest := filepath.Join(rolesDir, name+".md")
		if _, err := os.Stat(dest); err == nil {
			continue // already customized by the repo, leave it alone
		}
		content, err := Defaults.ReadFile("defaults/" + name + ".md")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			return nil, err
		}
		written = append(written, dest)
	}
	return written, nil
}
