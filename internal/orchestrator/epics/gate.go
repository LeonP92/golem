package epics

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leonp92/golem/internal/orchestrator/db"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SlotTTL is how long an integration slot survives its holder. A shem that
// crashes between the gate and the receipt stops blocking the run's merge
// lane when the slot lapses; the next gate check takes it over.
const SlotTTL = 5 * time.Minute

// GateOutcome is what the gate decided for one child.
type GateOutcome string

const (
	// GateGranted: the slot is yours; merge HeadSHA into TargetRef before
	// ExpiresAt, then post the receipt.
	GateGranted GateOutcome = "granted"
	// GateWait: another child holds the slot (or the run is paused); ask
	// again after RetryAfter.
	GateWait GateOutcome = "wait"
	// GateDone: the node already has its receipt — a retry after a crash.
	// Nothing to merge; nothing to post.
	GateDone GateOutcome = "done"
)

// Gate is the answer to one GateIntegration check.
type Gate struct {
	LeaseID    string
	Outcome    GateOutcome
	TargetRef  string        // granted: the ref to merge into
	ExpiresAt  time.Time     // granted: the slot lapses then
	RetryAfter time.Duration // wait: when to ask again
	Reason     string        // wait: why
}

// GateIntegration is the merge lane's gate: every precondition for a child
// to merge into the run's target ref, plus the per-run slot that serializes
// those merges. The shem that did the child's work calls it after the
// tester verdict passes; the CAS push on the remote is the backstop, this
// slot is what keeps two finished children from racing at all.
//
// The pin is (node, head SHA): the shem merges exactly the head the tester
// passed, and the slot is bound to it so a takeover never merges a stale
// head under a fresh one's lease.
func GateIntegration(gdb *gorm.DB, epicID, nodeKey, headSHA string, shemID uint, now time.Time) (Gate, error) {
	if nodeKey == "" || headSHA == "" {
		return Gate{}, fmt.Errorf("%w: the gate needs the node and its pushed head", ErrInvalid)
	}
	var gate Gate
	err := gdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.Epic{}).Where("id = ?", epicID).Update("updated_at", now).Error; err != nil {
			return err
		}
		var err error
		gate, err = integrationGate(tx, epicID, nodeKey, headSHA, shemID, now)
		return err
	})
	return gate, err
}

func integrationGate(tx *gorm.DB, epicID, nodeKey, headSHA string, shemID uint, now time.Time) (Gate, error) {
	e, err := load(tx, epicID)
	if err != nil {
		return Gate{}, err
	}
	var child db.Ticket
	if err := tx.Where("epic_id = ? AND epic_node_key = ?", epicID, nodeKey).First(&child).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Gate{}, fmt.Errorf("%w: node %q is not in the approved plan", ErrInvalid, nodeKey)
		}
		return Gate{}, err
	}
	if child.EpicIntegratedSHA != "" {
		return Gate{Outcome: GateDone}, nil
	}
	switch e.State {
	case StatePaused:
		return Gate{Outcome: GateWait, RetryAfter: SlotTTL, Reason: "run is paused"}, nil
	case StateRunning:
	default:
		return Gate{}, fmt.Errorf("%w: run is %s, not running", ErrWrongState, e.State)
	}
	if !e.AutoMergeChildren {
		return Gate{}, fmt.Errorf("%w: this run does not integrate children itself; a human records the merge", ErrInvalid)
	}
	if child.Phase != "ready-for-review" {
		return Gate{}, fmt.Errorf("%w: node %q is %s, not ready-for-review", ErrWrongState, nodeKey, child.Phase)
	}
	if e.TesterRequired || child.TesterRequired {
		if v := LatestVerdict(tx, epicID, nodeKey, headSHA); v == nil || v.Verdict != "pass" {
			return Gate{}, fmt.Errorf("%w: no tester pass for head %s", ErrInvalid, headSHA)
		}
	}

	// The slot row's primary key is the epic: one holder per run. INSERT
	// takes an empty lane. An identical live request reuses its lease;
	// every expiry takeover gets a new identity, including the same head.
	expires := now.Add(SlotTTL)
	slot := db.EpicIntegrationSlot{LeaseID: uuid.NewString(), EpicID: epicID, NodeKey: nodeKey, HeadSHA: headSHA, ShemID: shemID, ExpiresAt: expires}
	res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&slot)
	if res.Error != nil {
		return Gate{}, res.Error
	}
	if res.RowsAffected == 0 {
		var cur db.EpicIntegrationSlot
		if err := tx.First(&cur, "epic_id = ?", epicID).Error; err != nil {
			return Gate{}, err
		}
		if cur.ExpiresAt.After(now) {
			if cur.NodeKey == nodeKey && cur.HeadSHA == headSHA && cur.ShemID == shemID && cur.LeaseID != "" {
				return Gate{Outcome: GateGranted, TargetRef: TargetRef(e), ExpiresAt: cur.ExpiresAt, LeaseID: cur.LeaseID}, nil
			}
			return Gate{Outcome: GateWait, Reason: fmt.Sprintf("node %q is integrating", cur.NodeKey), RetryAfter: cur.ExpiresAt.Sub(now)}, nil
		}
		if err := tx.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ?", epicID).
			Updates(map[string]any{"lease_id": slot.LeaseID, "node_key": nodeKey, "head_sha": headSHA, "shem_id": shemID, "expires_at": expires}).Error; err != nil {
			return Gate{}, err
		}
	}
	return Gate{Outcome: GateGranted, TargetRef: TargetRef(e), ExpiresAt: expires, LeaseID: slot.LeaseID}, nil
}

// ApplyIntegrationResult consumes the exact lease and applies its outcome in
// one transaction. Replaced operations cannot mutate newer work. An identical
// successful receipt is a no-op, even after completion.
func ApplyIntegrationResult(gdb *gorm.DB, pin db.EpicIntegrationSlot, mergeSHA string, apply func(*gorm.DB, db.Ticket) error) error {
	return gdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.Epic{}).Where("id = ?", pin.EpicID).Update("updated_at", time.Now()).Error; err != nil {
			return err
		}
		var child db.Ticket
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("epic_id = ? AND epic_node_key = ?", pin.EpicID, pin.NodeKey).First(&child).Error; err != nil {
			return err
		}
		if child.AssignedShem == nil || *child.AssignedShem != pin.ShemID {
			return ErrIntegrationConflict
		}
		if child.EpicIntegratedSHA != "" {
			if mergeSHA != "" && mergeSHA == child.EpicIntegratedSHA {
				return nil
			}
			return ErrIntegrationConflict
		}
		e, err := load(tx, pin.EpicID)
		if err != nil {
			return err
		}
		if !e.AutoMergeChildren || (e.State != StateRunning && e.State != StatePaused) || child.Phase != "ready-for-review" {
			return ErrWrongState
		}
		if pin.LeaseID == "" || pin.HeadSHA == "" {
			return ErrIntegrationConflict
		}
		if e.TesterRequired || child.TesterRequired {
			if v := LatestVerdict(tx, pin.EpicID, pin.NodeKey, pin.HeadSHA); v == nil || v.Verdict != "pass" {
				return ErrIntegrationConflict
			}
		}
		res := tx.Where("epic_id = ? AND node_key = ? AND head_sha = ? AND shem_id = ? AND lease_id = ? AND expires_at > ?",
			pin.EpicID, pin.NodeKey, pin.HeadSHA, pin.ShemID, pin.LeaseID, time.Now()).Delete(&db.EpicIntegrationSlot{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrIntegrationConflict
		}
		return apply(tx, child)
	})
}
