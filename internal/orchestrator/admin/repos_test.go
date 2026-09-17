package admin_test

import (
	"errors"
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/admin"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

func intPtrRepos(n int) *int { return &n }

// mustSeed creates the repo row, and when withTickets is set, a ticket from
// that remote plus an outbox row queued behind it.
func mustSeed(t *testing.T, gdb *gorm.DB, remote string, withTickets bool) {
	t.Helper()
	if err := gdb.Create(&db.GitHubRepo{
		RepoRemote: remote, Owner: "acme", Name: "r", Label: "golem",
	}).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	if !withTickets {
		return
	}
	if err := gdb.Create(&db.Ticket{
		ID: "t-1", RepoRemote: remote, Branch: "ticket/t-1",
		Description: "d", Phase: "unassigned", IssueNumber: intPtrRepos(7),
	}).Error; err != nil {
		t.Fatalf("seed ticket: %v", err)
	}
	if err := gdb.Create(&db.GitHubOutbox{
		TicketID: "t-1", Kind: "label", IdempotencyKey: "t-1:label:unassigned", Payload: "{}",
	}).Error; err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
}

// Removing a repo had no implementation at all before this: not in the UI,
// not in the CLI, not here. A row saved once was permanent short of opening
// the database file by hand, which is how an operator ends up looking at a
// repository on /settings/github that appears in no configuration they own.
func TestReposRemove(t *testing.T) {
	const withTickets = "https://github.com/acme/stale"
	const empty = "https://github.com/acme/empty"

	t.Run("a repo with tickets is refused without force", func(t *testing.T) {
		gdb := openTestDB(t)
		mustSeed(t, gdb, withTickets, true)
		if err := admin.ReposRemove(gdb, withTickets, false); !errors.Is(err, admin.ErrRepoInUse) {
			t.Fatalf("err = %v, want ErrRepoInUse", err)
		}
		var n int64
		gdb.Model(&db.GitHubRepo{}).Where("repo_remote = ?", withTickets).Count(&n)
		if n != 1 {
			t.Error("the refused removal deleted the row anyway")
		}
	})

	t.Run("a repo with no tickets is removed", func(t *testing.T) {
		gdb := openTestDB(t)
		mustSeed(t, gdb, empty, false)
		if err := admin.ReposRemove(gdb, empty, false); err != nil {
			t.Fatalf("ReposRemove: %v", err)
		}
		var n int64
		gdb.Model(&db.GitHubRepo{}).Where("repo_remote = ?", empty).Count(&n)
		if n != 0 {
			t.Error("the repo row survived removal")
		}
	})

	t.Run("force removes the row and the outbox rows queued behind it", func(t *testing.T) {
		gdb := openTestDB(t)
		mustSeed(t, gdb, withTickets, true)
		if err := admin.ReposRemove(gdb, withTickets, true); err != nil {
			t.Fatalf("ReposRemove --force: %v", err)
		}
		var repos, outbox int64
		gdb.Model(&db.GitHubRepo{}).Where("repo_remote = ?", withTickets).Count(&repos)
		gdb.Model(&db.GitHubOutbox{}).Count(&outbox)
		if repos != 0 {
			t.Error("the repo row survived a forced removal")
		}
		// Left behind, these retry forever against settings that no longer
		// exist — the reason the removal clears them in the same transaction.
		if outbox != 0 {
			t.Errorf("outbox rows = %d, want 0: queued writes outlived their repo", outbox)
		}
	})

	t.Run("an unknown remote says so rather than reporting success", func(t *testing.T) {
		gdb := openTestDB(t)
		err := admin.ReposRemove(gdb, "https://github.com/acme/nope", false)
		if err == nil {
			t.Fatal("removing a remote with no row reported success")
		}
	})

	t.Run("list reflects what remains", func(t *testing.T) {
		gdb := openTestDB(t)
		mustSeed(t, gdb, empty, false)
		repos, err := admin.ReposList(gdb)
		if err != nil {
			t.Fatalf("ReposList: %v", err)
		}
		if len(repos) != 1 || repos[0].RepoRemote != empty {
			t.Fatalf("ReposList = %+v, want just %s", repos, empty)
		}
		if err := admin.ReposRemove(gdb, empty, false); err != nil {
			t.Fatalf("ReposRemove: %v", err)
		}
		repos, err = admin.ReposList(gdb)
		if err != nil {
			t.Fatalf("ReposList after removal: %v", err)
		}
		if len(repos) != 0 {
			t.Errorf("ReposList = %+v, want empty", repos)
		}
	})
}
