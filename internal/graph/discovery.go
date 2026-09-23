package graph

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var defaultExtensions = map[string]bool{
	".go": true, ".py": true, ".ts": true, ".js": true, ".tsx": true,
	".jsx": true, ".mjs": true, ".cjs": true, ".rs": true, ".java": true,
	".kt": true, ".swift": true, ".rb": true, ".php": true, ".cs": true,
	".cpp": true, ".c": true, ".h": true, ".ex": true, ".exs": true,
	".hs": true, ".ml": true, ".scala": true, ".clj": true, ".cr": true,
	".zig": true, ".lua": true, ".r": true, ".jl": true,
}

var defaultSkips = []string{
	".git/", ".golem/", "node_modules/", "vendor/", "__pycache__/",
	"dist/", "build/", "target/", ".venv/",
}

type Module struct {
	Path  string
	Files []string
}

func Discover(repoRoot string, maxFileSizeKB int, extraExts, ignorePatterns []string) ([]Module, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, gitListError(repoRoot, err)
	}

	exts := extSet(extraExts)
	skips := append(defaultSkips, ignorePatterns...)
	maxBytes := int64(maxFileSizeKB) * 1024
	if maxFileSizeKB == 0 {
		maxBytes = 200 * 1024
	}

	byDir := map[string][]string{}
	for _, rel := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if rel == "" || skipped(rel, skips) || !exts[filepath.Ext(rel)] {
			continue
		}
		info, err := os.Stat(filepath.Join(repoRoot, rel))
		if err != nil || info.Size() > maxBytes {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		byDir[dir] = append(byDir[dir], rel)
	}

	modules := make([]Module, 0, len(byDir))
	for dir, files := range byDir {
		modules = append(modules, Module{Path: dir, Files: files})
	}
	return modules, nil
}

func extSet(extra []string) map[string]bool {
	m := make(map[string]bool, len(defaultExtensions)+len(extra))
	for k, v := range defaultExtensions {
		m[k] = v
	}
	for _, e := range extra {
		m[e] = true
	}
	return m
}

func skipped(rel string, skips []string) bool {
	for _, s := range skips {
		if strings.HasPrefix(rel, s) || strings.Contains(rel, "/"+s) {
			return true
		}
	}
	return false
}

// gitListError turns git's exit status into something that names the cause.
//
// cmd.Output() already captures git's stderr onto the *exec.ExitError, but
// returning the bare error discarded it, so callers printed only
//
//	discovering modules: exit status 128
//
// That is git's code for "not a git repository", and the message saying so
// was sitting on the error unread — including in the shem's logs, where this
// appeared as a repo pre-flight warning with nothing to indicate that the
// directory simply had no repository in it.
func gitListError(repoRoot string, err error) error {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || len(exitErr.Stderr) == 0 {
		return fmt.Errorf("git ls-files in %s: %w", repoRoot, err)
	}
	msg := strings.TrimSpace(string(exitErr.Stderr))
	if strings.Contains(msg, "not a git repository") {
		return fmt.Errorf("%s is not a git repository — golem graph needs one "+
			"(clone or git init there first): %s", repoRoot, msg)
	}
	return fmt.Errorf("git ls-files in %s: %w: %s", repoRoot, err, msg)
}
