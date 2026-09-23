package ghsync_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"gorm.io/gorm"
)

func TestGraphBuildClaim(t *testing.T) {
	const remote = "https://github.com/acme/widgets"
	now := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)

	openDB := func(t *testing.T) *gorm.DB {
		t.Helper()
		gdb, err := db.Open(":memory:")
		if err != nil {
			t.Fatalf("db.Open: %v", err)
		}
		return gdb
	}
	newDB := func(t *testing.T) *gorm.DB {
		t.Helper()
		gdb := openDB(t)
		if err := gdb.Create(&db.GitHubRepo{
			RepoRemote: remote, Owner: "acme", Name: "widgets", Label: "golem", Enabled: true,
		}).Error; err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		return gdb
	}

	t.Run("an unknown remote is reported, not silently started", func(t *testing.T) {
		gdb := openDB(t)
		_, err := ghsync.StartGraphBuild(gdb, "https://github.com/acme/nope", now)
		if !errors.Is(err, ghsync.ErrNoRepo) {
			t.Fatalf("err = %v, want ErrNoRepo", err)
		}
	})

	t.Run("a second request while one runs is refused", func(t *testing.T) {
		gdb := newDB(t)
		if _, err := ghsync.StartGraphBuild(gdb, remote, now); err != nil {
			t.Fatalf("first start: %v", err)
		}
		_, err := ghsync.StartGraphBuild(gdb, remote, now.Add(time.Minute))
		if !errors.Is(err, ghsync.ErrGraphBuildRunning) {
			t.Fatalf("err = %v, want ErrGraphBuildRunning", err)
		}
	})

	t.Run("a finished build frees the slot", func(t *testing.T) {
		gdb := newDB(t)
		if _, err := ghsync.StartGraphBuild(gdb, remote, now); err != nil {
			t.Fatalf("first start: %v", err)
		}
		if err := ghsync.FinishGraphBuild(gdb, remote, "", now.Add(time.Minute)); err != nil {
			t.Fatalf("finish: %v", err)
		}
		if _, err := ghsync.StartGraphBuild(gdb, remote, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("second start after finishing: %v", err)
		}
	})

	t.Run("a stale build frees the slot", func(t *testing.T) {
		// Otherwise a shem that died holding a build disables the button for
		// the life of the deployment — the same shape as a parked outbox row
		// with nothing able to un-park it.
		gdb := newDB(t)
		if _, err := ghsync.StartGraphBuild(gdb, remote, now); err != nil {
			t.Fatalf("first start: %v", err)
		}
		late := now.Add(db.GraphBuildTimeout + time.Minute)
		if _, err := ghsync.StartGraphBuild(gdb, remote, late); err != nil {
			t.Fatalf("start after the timeout: %v", err)
		}
	})

	t.Run("concurrent requests produce exactly one build", func(t *testing.T) {
		// The claim is the UPDATE's own WHERE clause rather than a read then
		// a write, so two operators pressing the button together cannot both
		// dispatch and have the shem run two builds over one checkout.
		gdb := newDB(t)
		const racers = 12
		var wg sync.WaitGroup
		var mu sync.Mutex
		var won, refused int
		wg.Add(racers)
		for i := 0; i < racers; i++ {
			go func() {
				defer wg.Done()
				_, err := ghsync.StartGraphBuild(gdb, remote, now)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					won++
				case errors.Is(err, ghsync.ErrGraphBuildRunning):
					refused++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}()
		}
		wg.Wait()
		if won != 1 {
			t.Errorf("%d racers won the claim, want exactly 1 (refused %d)", won, refused)
		}
	})

	t.Run("a failure is recorded with its text", func(t *testing.T) {
		gdb := newDB(t)
		if _, err := ghsync.StartGraphBuild(gdb, remote, now); err != nil {
			t.Fatalf("start: %v", err)
		}
		const boom = "claude --print failed: Failed to authenticate"
		if err := ghsync.FinishGraphBuild(gdb, remote, boom, now.Add(time.Minute)); err != nil {
			t.Fatalf("finish: %v", err)
		}
		var got db.GitHubRepo
		if err := gdb.Where("repo_remote = ?", remote).First(&got).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if got.GraphBuildError != boom {
			t.Errorf("GraphBuildError = %q, want the reported text", got.GraphBuildError)
		}
		if got.GraphBuildRunning(now.Add(2 * time.Minute)) {
			t.Error("a finished build still reads as running")
		}
	})
}
