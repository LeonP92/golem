package epics

import (
	"fmt"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
)

// RecordVerdict stores a tester verdict (append-only). A verdict is scoped
// to its candidate: an epic node (EpicID + EpicNodeKey) or a standalone
// ticket (TicketID) — exactly one of the two.
func RecordVerdict(gdb *gorm.DB, v db.TesterVerdict) error {
	if (v.EpicID == "") == (v.TicketID == "") || v.HeadSHA == "" {
		return fmt.Errorf("%w: a verdict needs exactly one scope (epic or ticket) and the tested head", ErrInvalid)
	}
	switch v.Verdict {
	case "pass", "fail", "inconclusive":
	default:
		return fmt.Errorf("%w: unknown verdict %q", ErrInvalid, v.Verdict)
	}
	return gdb.Create(&v).Error
}

// LatestVerdict returns the newest verdict for this exact head, or nil: a
// verdict for any other head describes code the candidate no longer is.
func LatestVerdict(gdb *gorm.DB, epicID, nodeKey, headSHA string) *db.TesterVerdict {
	var vs []db.TesterVerdict
	gdb.Where("epic_id = ? AND epic_node_key = ? AND head_sha = ?", epicID, nodeKey, headSHA).
		Order("id desc").Limit(1).Find(&vs)
	if len(vs) == 0 {
		return nil
	}
	return &vs[0]
}
