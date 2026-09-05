package soul

import (
	"os"
	"path/filepath"

	"github.com/leonp92/golem/internal/blog"
)

type Candidate struct {
	Source     blog.Entry
	Suggestion string
}

func ExtractCandidates(entries []blog.Entry) []Candidate {
	blockers := make(map[string]blog.Entry)
	for _, e := range entries {
		if e.Type == blog.TypeBlocker {
			blockers[e.ID] = e
		}
	}

	var candidates []Candidate
	for _, e := range entries {
		if e.Type != blog.TypeResolved || e.Role != "human" {
			continue
		}
		original, ok := blockers[e.InReplyTo]
		if !ok || original.Message == e.Message {
			continue
		}
		candidates = append(candidates, Candidate{
			Source:     e,
			Suggestion: e.Message,
		})
	}
	return candidates
}

func Promote(soulDir, filename, content string) error {
	if err := os.MkdirAll(soulDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(soulDir, filename), []byte(content), 0o644)
}
