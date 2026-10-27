package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/shem/config"
)

func TestWorktreeBootstrapLifecycle(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init")
	ticket := filepath.Join(repo, ".golem", "tickets", "t")
	wt := filepath.Join(ticket, "worktree")
	os.MkdirAll(wt, 0755)
	git(t, wt, "init")
	cfg := &config.Config{Repos: []config.RepoConfig{{NormalizedRemote: "r", WorktreeSetup: []string{"echo run >> count"}}}}
	e := &GolemExecutor{}
	for range 2 {
		if err := e.prepareWorktree(context.Background(), cfg, "r", ticket); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(wt, "count"))
	if string(b) != "run\n" {
		t.Fatalf("ran more than once: %q", b)
	}
	cfg.Repos[0].SetupTimeout = time.Minute // a new timeout alone must not reinstall
	if err := e.prepareWorktree(context.Background(), cfg, "r", ticket); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "count")); string(b) != "run\n" {
		t.Fatalf("timeout change reran setup: %q", b)
	}
	cfg.Repos[0].WorktreeSetup = []string{"echo changed >> count"}
	if err := e.prepareWorktree(context.Background(), cfg, "r", ticket); err != nil {
		t.Fatal(err)
	}
	cfg.Repos[0].WorktreeSetup = []string{"exit 7"}
	if err := e.prepareWorktree(context.Background(), cfg, "r", ticket); err == nil {
		t.Fatal("setup failure lost")
	}
	cfg.Repos[0].WorktreeSetup = []string{"sleep 2"}
	cfg.Repos[0].SetupTimeout = 10 * time.Millisecond
	if err := e.prepareWorktree(context.Background(), cfg, "r", ticket); err == nil {
		t.Fatal("setup timeout lost")
	}
}
func TestBootstrapCannotIntroduceRealEnv(t *testing.T) {
	dir := t.TempDir()
	wt := filepath.Join(dir, "worktree")
	os.MkdirAll(wt, 0755)
	git(t, wt, "init")
	cfg := &config.Config{Repos: []config.RepoConfig{{NormalizedRemote: "r", WorktreeSetup: []string{"echo synthetic > .env"}}}}
	if err := (&GolemExecutor{}).prepareWorktree(context.Background(), cfg, "r", dir); err == nil {
		t.Fatal("bootstrap introduced live env")
	}
	if _, err := os.Stat(filepath.Join(wt, ".git", "golem-worktree-setup")); !os.IsNotExist(err) {
		t.Fatal("failed setup marked successful")
	}
}
