package epics

import (
	"fmt"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// CompleteFinal records the human final merge. The merge to base is always
// a person's for epic-branch runs, so this is an
// attestation, not an action: the caller states that the integration branch
// is merged and sha is the resulting base head, and the run closes on that
// word. Repeating the same attestation is a no-op.
func CompleteFinal(gdb *gorm.DB, epicID, sha string) error {
	if sha == "" {
		return fmt.Errorf("%w: the attestation needs the final SHA on base", ErrInvalid)
	}
	e, err := load(gdb, epicID)
	if err != nil {
		return err
	}
	if e.State == StateCompleted {
		if e.FinalSHA == sha {
			return nil
		}
		return fmt.Errorf("%w: run is already completed with final SHA %s", ErrWrongState, e.FinalSHA)
	}
	if e.State != StateAwaitingFinalReview {
		return fmt.Errorf("%w: the final merge attestation needs %s, run is %s",
			ErrWrongState, StateAwaitingFinalReview, e.State)
	}
	res := gdb.Model(&db.Epic{}).Where("id = ? AND state = ?", epicID, StateAwaitingFinalReview).
		Updates(map[string]any{"state": StateCompleted, "final_sha": sha})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: run left %s before the attestation landed", ErrWrongState, StateAwaitingFinalReview)
	}
	return nil
}
