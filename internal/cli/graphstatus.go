package cli

import (
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leonpham/golem/internal/graph"
)

func GraphStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	indexDir := filepath.Join(*repo, ".golem", "index")
	meta, err := graph.LoadMeta(indexDir)
	if err != nil || meta.BaseCommit == "" {
		fmt.Fprintln(stdout, "no graph index — run 'golem graph build'")
		return 0
	}

	fmt.Fprintf(stdout, "indexed at commit %s\n%d modules\n\n", meta.BaseCommit[:8], len(meta.Modules))

	staleCount := 0
	for modPath, mod := range meta.Modules {
		for file, oldHash := range mod.FileHashes {
			newHash, err := graph.FileHash(filepath.Join(*repo, file))
			if err != nil || newHash != oldHash {
				fmt.Fprintf(stdout, "STALE  %s\n", modPath)
				staleCount++
				break
			}
		}
	}
	if staleCount == 0 {
		fmt.Fprintln(stdout, "all modules current")
	} else {
		fmt.Fprintf(stdout, "\n%d stale module(s) — run 'golem graph update'\n", staleCount)
	}
	return 0
}

func gitHead(repoRoot string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
