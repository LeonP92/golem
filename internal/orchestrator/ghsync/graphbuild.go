package ghsync

import (
	"errors"
	"fmt"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// ErrGraphBuildRunning is returned when a build for that repository is
// already in flight and has not gone stale.
var ErrGraphBuildRunning = errors.New("a graph build is already running for this repository")

// ErrNoRepo is returned when there is no repo row for the remote.
var ErrNoRepo = errors.New("no repo row for that remote")

// StartGraphBuild claims the graph-build slot for a repository.
//
// The claim is the UPDATE's own WHERE clause rather than a read followed by a
// write: two operators pressing the button at the same moment would otherwise
// both see "not running" and both dispatch, and the shem would run two builds
// over the same checkout. RowsAffected == 0 means somebody else won.
func StartGraphBuild(gdb *gorm.DB, remote string, now time.Time) (db.GitHubRepo, error) {
	var found []db.GitHubRepo
	if err := gdb.Where("repo_remote = ?", remote).Limit(1).Find(&found).Error; err != nil {
		return db.GitHubRepo{}, fmt.Errorf("loading %s: %w", remote, err)
	}
	if len(found) == 0 {
		return db.GitHubRepo{}, fmt.Errorf("%w: %s", ErrNoRepo, remote)
	}

	stale := now.Add(-db.GraphBuildTimeout)
	res := gdb.Model(&db.GitHubRepo{}).
		Where("repo_remote = ?", remote).
		// Free when no build was ever started, when the last one finished, or
		// when the one holding it started long enough ago that the shem
		// carrying it is gone. That last clause is what stops a crashed build
		// disabling the button forever.
		Where("graph_build_started_at IS NULL OR "+
			"(graph_build_finished_at IS NOT NULL AND graph_build_finished_at >= graph_build_started_at) OR "+
			"graph_build_started_at < ?", stale).
		Updates(map[string]any{
			"graph_build_started_at":  now,
			"graph_build_finished_at": nil,
			"graph_build_error":       "",
		})
	if res.Error != nil {
		return db.GitHubRepo{}, fmt.Errorf("claiming the graph build for %s: %w", remote, res.Error)
	}
	if res.RowsAffected == 0 {
		return found[0], ErrGraphBuildRunning
	}
	return found[0], nil
}

// FinishGraphBuild records the outcome. A non-empty buildErr marks it failed.
//
// Keyed on the remote alone, deliberately: the shem reports whatever it ran,
// and if an operator re-triggered in the meantime the later start wins the
// display anyway because this only writes the finish columns.
func FinishGraphBuild(gdb *gorm.DB, remote, buildErr string, now time.Time) error {
	res := gdb.Model(&db.GitHubRepo{}).
		Where("repo_remote = ?", remote).
		Updates(map[string]any{
			"graph_build_finished_at": now,
			"graph_build_error":       buildErr,
		})
	if res.Error != nil {
		return fmt.Errorf("recording the graph build result for %s: %w", remote, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", ErrNoRepo, remote)
	}
	return nil
}
