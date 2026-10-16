package epics

import (
	"fmt"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// The tester verdict is the only evidence that authorizes an auto-merge.
// This file owns recording it and reading it back for the lane.

// RecordVerdict stores the tester's verdict for one merge candidate and
// lifts the lane's blocked-tester row for the same head, so the next
// monitor pass re-evaluates with the verdict in hand. The row is
// append-only: a re-test of a moved head is a new row, and the history of
// what was tried stays.
func RecordVerdict(gdb *gorm.DB, v db.TesterVerdict) error {
	if v.EpicID == "" || v.HeadSHA == "" {
		return fmt.Errorf("epics: a verdict needs its epic and the tested head")
	}
	switch v.Verdict {
	case "pass", "fail", "inconclusive":
	default:
		return fmt.Errorf("epics: unknown verdict %q", v.Verdict)
	}
	return gdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&v).Error; err != nil {
			return err
		}
		// A verdict answers the question the blocked row was waiting on.
		// Deleting the block lets the lane re-attempt this head; the
		// verdict itself decides whether that attempt merges.
		return tx.Where("epic_id = ? AND epic_node_key = ? AND head_sha = ? AND state = ?",
			v.EpicID, v.EpicNodeKey, v.HeadSHA, "blocked-tester").
			Delete(&db.IntegrationAttempt{}).Error
	})
}

// latestVerdict returns the newest verdict for this exact head, or nil.
// Matching on the head is what makes a stale-SHA verdict inert: it
// describes code the candidate no longer contains.
func latestVerdict(gdb *gorm.DB, epicID, nodeKey, headSHA string) *db.TesterVerdict {
	var v db.TesterVerdict
	if err := gdb.Where("epic_id = ? AND epic_node_key = ? AND head_sha = ?",
		epicID, nodeKey, headSHA).Order("id desc").First(&v).Error; err != nil {
		return nil
	}
	return &v
}
