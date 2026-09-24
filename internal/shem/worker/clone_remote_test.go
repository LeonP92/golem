package worker

import (
	"testing"

	"github.com/leonp92/golem/internal/shem/config"
)

// A clone must use the URL the operator configured, not the normalized one.
//
// Normalization lowercases and strips ".git" — fine for identity, wrong for
// fetching. GitHub serves both spellings; a plain HTTP host serves only the
// path that exists, so the on-demand clone 404s.
func TestRepoCloneRemoteUsesTheConfiguredURL(t *testing.T) {
	cfg := &config.Config{Repos: []config.RepoConfig{
		{Path: "/repos/a", Remote: "https://git.example.com/Org/Repo.git",
			NormalizedRemote: "https://git.example.com/org/repo"},
	}}

	if got := repoCloneRemote(cfg, "https://git.example.com/org/repo"); got != "https://git.example.com/Org/Repo.git" {
		t.Errorf("clone remote = %q, want the configured URL; cloning the normalized "+
			"form drops .git and the case, which not every host serves", got)
	}

	// An unconfigured repo has nothing better to offer than what it was given.
	if got := repoCloneRemote(cfg, "https://git.example.com/org/other"); got != "https://git.example.com/org/other" {
		t.Errorf("unconfigured remote = %q, want it returned unchanged", got)
	}
}
