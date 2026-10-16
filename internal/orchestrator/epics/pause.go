package epics

import (
	"errors"
	"fmt"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// Pause and Resume are the run-level stop. Pause is honored everywhere
// without a gate of its own: every claim path requires state 'running'
// (epicgate.go re-checks the epic row, not a cached flag) and the
// integration lane no-ops unless the epic is running, so a paused run
// stops claiming and merging at the next claim or merge attempt — work
// already claimed finishes, which is what makes pause safe to press.
//
// Resume returns the run to 'running' from 'paused' only: a run in
// needs-attention has a named cause a human must resolve, and resume
// must never wave one past.
func Pause(gdb *gorm.DB, epicID string) error {
	res := gdb.Model(&db.Epic{}).Where("id = ? AND state = ?", epicID, "running").
		Update("state", "paused")
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return pauseStateError(gdb, epicID, "pause")
	}
	return nil
}

func Resume(gdb *gorm.DB, epicID string) error {
	res := gdb.Model(&db.Epic{}).Where("id = ? AND state = ?", epicID, "paused").
		Update("state", "running")
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return pauseStateError(gdb, epicID, "resume")
	}
	return nil
}

func pauseStateError(gdb *gorm.DB, epicID, op string) error {
	var e db.Epic
	if err := gdb.First(&e, "id = ?", epicID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	return fmt.Errorf("epics: cannot %s a run in state %q", op, e.State)
}
