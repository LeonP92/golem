package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/evidence"
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

func TestWorktreeSecretCheckWithoutSetupOrWithCachedSetup(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			repo := t.TempDir()
			ticketDir := filepath.Join(repo, ".golem", "tickets", "one")
			worktree := filepath.Join(ticketDir, "worktree")
			if err := os.MkdirAll(worktree, 0755); err != nil {
				t.Fatal(err)
			}
			git(t, worktree, "init")
			cfg := &config.Config{Repos: []config.RepoConfig{{NormalizedRemote: "r"}}}
			e := &GolemExecutor{}
			if cached {
				cfg.Repos[0].WorktreeSetup = []string{"echo run >> count"}
				if err := e.prepareWorktree(context.Background(), cfg, "r", ticketDir); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(worktree, ".env"), []byte("SYNTHETIC=true"), 0600); err != nil {
				t.Fatal(err)
			}
			// The parent scan intentionally skips .golem; the active worktree must be checked itself.
			if err := evidence.CheckCheckout(repo); err != nil {
				t.Fatal(err)
			}
			err := e.prepareWorktree(context.Background(), cfg, "r", ticketDir)
			if err == nil || !strings.HasPrefix(err.Error(), evidence.EnvironmentMarker) {
				t.Fatalf("worktree secret not rejected: %v", err)
			}
			if cached {
				b, err := os.ReadFile(filepath.Join(worktree, "count"))
				if err != nil || string(b) != "run\n" {
					t.Fatalf("cached setup reran: %q %v", b, err)
				}
			}
		})
	}
}
