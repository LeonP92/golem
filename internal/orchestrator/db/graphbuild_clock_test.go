package db_test

import (
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

// A build that finishes in the same clock tick it started is finished.
// Equal timestamps are common on a coarse clock; the row used to read
// "building…" for the whole 45-minute timeout.
func TestGraphBuildFinishedInTheSameTickIsNotRunning(t *testing.T) {
	now := time.Now().UTC()
	repo := db.GitHubRepo{GraphBuildStartedAt: &now, GraphBuildFinishedAt: &now}

	if repo.GraphBuildRunning(now) {
		t.Error("a build whose finish timestamp equals its start reads as still running; " +
			"the dashboard shows \"building…\" for the full timeout")
	}
	if repo.GraphBuildStale(now.Add(db.GraphBuildTimeout * 2)) {
		t.Error("a finished build went stale; stale is for builds that never reported back")
	}
}
