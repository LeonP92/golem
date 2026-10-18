package epics

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/slug"
)

var (
	ErrNotApproved = errors.New("epics: no approved plan revision")
	ErrWrongState  = errors.New("epics: wrong run state for this operation")
	// ErrIntegrationConflict: a second, different receipt for one node.
	ErrIntegrationConflict = errors.New("epics: node already integrated with different content")
)

// Start materializes the approved plan into ordinary child tickets and
// moves the run to running. Child IDs are deterministic, so a repeated
// start creates nothing.
func Start(gdb *gorm.DB, epicID string) (int, error) {
	created := 0
	err := gdb.Transaction(func(tx *gorm.DB) error {
		e, err := load(tx, epicID)
		if err != nil {
			return err
		}
		if e.ApprovedRevision == 0 {
			return ErrNotApproved
		}
		if e.State != StateAwaitingApproval && e.State != StateRunning {
			return ErrWrongState
		}
		plan, _, err := LoadPlan(tx, e.ID, e.ApprovedRevision)
		if err != nil {
			return err
		}
		for _, n := range plan.Nodes {
			id := fmt.Sprintf("epic-%s-%s", e.ID[:8], strings.ToLower(n.Key))
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&db.Ticket{
				ID:               id,
				RepoRemote:       e.RepoRemote,
				BaseBranch:       TargetRef(e),
				Title:            n.Title,
				Branch:           slug.Branch(n.Title, id),
				Description:      childDescription(plan, n),
				Phase:            "unassigned",
				EpicID:           &e.ID,
				EpicNodeKey:      n.Key,
				EpicPlanRevision: e.ApprovedRevision,
				ManualApproval:   !e.AutoApprovePhases, // snapshot; later policy edits never reach it
				// The tester gate is a ticket-level flag the run composes:
				// snapshotted like ManualApproval, so a later policy edit
				// never silently changes an existing child's gate.
				TesterRequired: e.TesterRequired,
			})
			if res.Error != nil {
				return res.Error
			}
			created += int(res.RowsAffected)
		}
		if err := tx.Model(&db.Epic{}).Where("id = ? AND state = ?", e.ID, StateAwaitingApproval).
			Update("state", StateRunning).Error; err != nil {
			return err
		}
		_, err = reconcileReadiness(tx, e.ID, plan)
		return err
	})
	return created, err
}

// childDescription is what the implementing agent reads, verbatim from the
// approved plan.
func childDescription(plan *epic.Plan, n epic.Node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", n.Title)
	if n.Scope != "" {
		fmt.Fprintf(&b, "Scope: %s\n\n", n.Scope)
	}
	if n.NonGoals != "" {
		fmt.Fprintf(&b, "Non-goals: %s\n\n", n.NonGoals)
	}
	b.WriteString("Part of an epic run. Requirements this ticket must satisfy:\n")
	for _, rid := range n.RequirementIDs {
		for _, r := range plan.Requirements {
			if r.ID == rid {
				fmt.Fprintf(&b, "- %s: %s\n", r.ID, r.Text)
			}
		}
	}
	b.WriteString("\nAcceptance commands (all must pass):\n")
	for _, c := range n.AcceptanceCommands {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	return b.String()
}

// reconcileReadiness recomputes every child's EpicReady from the receipts
// on the children and reports whether every node is integrated.
func reconcileReadiness(tx *gorm.DB, epicID string, plan *epic.Plan) (allIntegrated bool, err error) {
	var kids []db.Ticket
	if err := tx.Select("id", "epic_node_key", "epic_integrated_sha").
		Where("epic_id = ?", epicID).Find(&kids).Error; err != nil {
		return false, err
	}
	integrated := map[string]bool{}
	for _, k := range kids {
		integrated[k.EpicNodeKey] = k.EpicIntegratedSHA != ""
	}
	ready := epic.Ready(*plan, integrated)
	for _, k := range kids {
		if integrated[k.EpicNodeKey] {
			continue
		}
		if err := tx.Model(&db.Ticket{}).Where("id = ?", k.ID).
			Update("epic_ready", ready[k.EpicNodeKey]).Error; err != nil {
			return false, err
		}
	}
	for _, n := range plan.Nodes {
		if !integrated[n.Key] {
			return false, nil
		}
	}
	return true, nil
}

// RecordIntegration writes the receipt that a node's code merged into the
// run's target ref, unblocks successors, and moves a fully integrated run
// on. An identical repeat is a no-op; a different SHA is a conflict.
//
// The receipt releases the run's integration slot. emit hooks fire inside
// the same transaction when the run reaches awaiting-final-review — the
// GitHub add-on hangs its final-pull-request write on one; the core passes
// none and behaves identically.
func RecordIntegration(gdb *gorm.DB, epicID, nodeKey, sha string, emit ...func(tx *gorm.DB, e *db.Epic) error) error {
	if sha == "" {
		return fmt.Errorf("%w: integration receipt needs the merged SHA", ErrInvalid)
	}
	return gdb.Transaction(func(tx *gorm.DB) error {
		// Serialize receipt writes before reading siblings, including on PostgreSQL.
		if err := tx.Model(&db.Epic{}).Where("id = ?", epicID).Update("updated_at", time.Now()).Error; err != nil {
			return err
		}
		e, err := load(tx, epicID)
		if err != nil {
			return err
		}
		var child db.Ticket
		if err := tx.Where("epic_id = ? AND epic_node_key = ?", epicID, nodeKey).First(&child).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: node %q is not in the approved plan", ErrInvalid, nodeKey)
			}
			return err
		}
		// The repeat check comes before the state check on purpose: an
		// identical receipt is a no-op even after the run moved on — a
		// shem retrying after a crash lands here, and retrying must never
		// fail. A DIFFERENT sha still conflicts in any state.
		switch child.EpicIntegratedSHA {
		case sha:
			return nil
		case "":
		default:
			return ErrIntegrationConflict
		}
		if e.State != StateRunning && e.State != StatePaused {
			return ErrWrongState
		}
		if err := tx.Where("epic_id = ? AND node_key = ?", epicID, nodeKey).Delete(&db.EpicIntegrationSlot{}).Error; err != nil {
			return err
		}
		if err := tx.Model(&child).Update("epic_integrated_sha", sha).Error; err != nil {
			return err
		}
		plan, _, err := LoadPlan(tx, epicID, e.ApprovedRevision)
		if err != nil {
			return err
		}
		all, err := reconcileReadiness(tx, epicID, plan)
		if err != nil || !all {
			return err
		}
		next := StateAwaitingFinalReview
		if e.BranchStrategy == StrategyTrunkFlags {
			next = StateCompleted // already on base: that was the delivery
		}
		if err := tx.Model(&db.Epic{}).Where("id = ? AND state IN ?", epicID, []string{StateRunning, StatePaused}).
			Update("state", next).Error; err != nil {
			return err
		}
		if next != StateAwaitingFinalReview {
			return nil
		}
		for _, f := range emit {
			if err := f(tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// Pause stops claims and merges at the next attempt; work already claimed
// finishes. Resume undoes it.
func Pause(gdb *gorm.DB, epicID string) error {
	return transition(gdb, epicID, StateRunning, StatePaused)
}

func Resume(gdb *gorm.DB, epicID string) error {
	return transition(gdb, epicID, StatePaused, StateRunning)
}

func transition(gdb *gorm.DB, epicID, from, to string) error {
	res := gdb.Model(&db.Epic{}).Where("id = ? AND state = ?", epicID, from).Update("state", to)
	if res.Error != nil || res.RowsAffected == 1 {
		return res.Error
	}
	e, err := load(gdb, epicID)
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: run is %s, not %s", ErrWrongState, e.State, from)
}
