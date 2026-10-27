package evidence

import (
	"fmt"
	"io/fs"
	"path/filepath"
)

// CheckCheckout refuses live secrets in a shared checkout before handing its
// ownership to an agent. It does not inspect secret values or traverse symlinks.
// Secrets outside this tree still require a deployment filesystem boundary.
func CheckCheckout(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf(EnvironmentMarker+" cannot inspect checkout: %w", err)
		}
		if d.IsDir() {
			if skipCheckoutDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if SensitivePath(path) {
			rel, _ := filepath.Rel(root, path)
			return fmt.Errorf(EnvironmentMarker+" live secret path %s in agent checkout; move secrets outside the agent-readable tree and use synthetic test fixtures", rel)
		}
		return nil
	})
}

// skipCheckoutDir leaves out directories that hold dependencies, build output and other agents' worktrees: a certifi cacert.pem in a
// virtualenv or a testdata server.key is not a live secret, and .golem/tickets/*/worktree is checked on its own.
func skipCheckoutDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".golem", ".venv", "venv", "vendor", "target", "testdata", "__pycache__":
		return true
	}
	return false
}
