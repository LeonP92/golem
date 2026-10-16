package epics

// Run start and orchestration readiness: turn an approved plan into
// ordinary child tickets exactly once, and keep each child's readiness
// flag honest as integration receipts land.

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/slug"
)

var (
	// ErrNotApproved means start was attempted without an approved plan
	// revision — approval is what turns a plan into runnable work.
	ErrNotApproved = errors.New("epics: no approved plan revision")
	// ErrWrongState means the run is not in a state that accepts the
	// operation (start from awaiting-approval/running; receipts while
	// running).
	ErrWrongState = errors.New("epics: wrong run state for this operation")
	// ErrIntegrationConflict means a receipt arrived for a node that
	// already has one with different content — a second merge of the same
	// node is a fact to reconcile, not to overwrite silently.
	ErrIntegrationConflict = errors.New("epics: node already integrated with different content")
)

// Start materializes the approved plan revision into child tickets, once.
// Re-entering (a repeated click, a restarted orchestrator, a recovered
// crash mid-run) finds the same deterministic ticket IDs already present
// and creates nothing: the unique (epic_id, epic_node_key) index and the
// conditional state update make duplication impossible rather than
// unlikely.
//
// Children are ordinary tickets — the existing shems, phases, gates and
// dashboard all apply untouched. What makes them epic work travels on the
// ticket (epic id, node key, plan revision) and in the claim predicates,
// never in a parallel runtime.
func Start(gdb *gorm.DB, epicID string) (int, error) {
	created := 0
	err := gdb.Transaction(func(tx *gorm.DB) error {
		var e db.Epic
		if err := tx.Where("id = ?", epicID).First(&e).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if e.ApprovedRevision == 0 {
			return ErrNotApproved
		}
		if e.State != "awaiting-approval" && e.State != "running" {
			return ErrWrongState
		}
		plan, _, err := LoadPlan(tx, e.ID, e.ApprovedRevision)
		if err != nil {
			return err
		}
		for _, n := range plan.Nodes {
			// Deterministic IDs are the idempotency key: the same approved
			// revision always produces the same ticket ID for a node, so a
			// retried start collides on the primary key and skips.
			id := fmt.Sprintf("epic-%s-%s", e.ID[:8], strings.ToLower(n.Key))
			t := db.Ticket{
				ID:               id,
				RepoRemote:       e.RepoRemote,
				BaseBranch:       n.BranchTarget,
				Title:            n.Title,
				Branch:           slug.Branch(n.Title, id),
				Description:      childDescription(plan, n),
				Phase:            "unassigned",
				EpicID:           &e.ID,
				EpicNodeKey:      n.Key,
				EpicPlanRevision: e.ApprovedRevision,
				// The phase-approval policy is inherited here, at
				// materialization, and never re-read from the epic:
				// a child that exists keeps the mode it was created
				// with, so changing the run's policy mid-flight can
				// only reach future materializations.
				ManualApproval: !e.AutoApprovePhases,
			}
			res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&t)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				created++
			}
			// The node -> ticket mapping is written regardless of who won
			// the insert, so a crash between the two never leaves a node
			// pointing at nothing.
			if err := tx.Model(&db.EpicNode{}).
				Where("epic_id = ? AND revision = ? AND node_key = ? AND ticket_id = ''",
					e.ID, e.ApprovedRevision, n.Key).
				Update("ticket_id", id).Error; err != nil {
				return err
			}
		}
		// The state flip is conditional: only the first start moves
		// awaiting-approval -> running; a re-entry under "running" is a
		// no-op, and any other state was rejected above.
		if err := tx.Model(&db.Epic{}).
			Where("id = ? AND state = 'awaiting-approval'", e.ID).
			Update("state", "running").Error; err != nil {
			return err
		}
		return reconcileReadiness(tx, &e, plan)
	})
	return created, err
}

// childDescription is what the implementing agent reads: scope, the
// approved acceptance commands, and the requirement texts this node
// covers, verbatim from the approved plan — never a paraphrase.
func childDescription(plan *epic.Plan, n epic.Node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nPart of an epic run. Requirements this ticket must satisfy:\n", n.Title)
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

// reconcileReadiness recomputes every child's EpicReady flag from the
// approved plan and the receipts, at start and after every receipt. It
// derives the flags, never remembers them, so repeats are harmless.
func reconcileReadiness(gdb *gorm.DB, e *db.Epic, plan *epic.Plan) error {
	var nodes []db.EpicNode
	if err := gdb.Where("epic_id = ? AND revision = ?", e.ID, e.ApprovedRevision).Find(&nodes).Error; err != nil {
		return err
	}
	integrated := map[string]bool{}
	for _, n := range nodes {
		integrated[n.NodeKey] = n.IntegratedSHA != ""
	}
	ready := map[string]bool{}
	for _, key := range epic.Ready(*plan, epic.State{Integrated: integrated}) {
		ready[key] = true
	}
	for _, n := range nodes {
		if integrated[n.NodeKey] {
			continue // integrated nodes have no readiness to offer
		}
		if err := gdb.Model(&db.Ticket{}).
			Where("epic_id = ? AND epic_node_key = ?", e.ID, n.NodeKey).
			Update("epic_ready", ready[n.NodeKey]).Error; err != nil {
			return err
		}
	}
	return nil
}

// RecordIntegration writes the receipt that a node's code has merged into
// its branch target — in M2 a human merged the child's PR, and this is
// how the run learns. The receipt is what unblocks successors; a closed
// ticket or a ready-for-review PR is deliberately not enough.
//
// Idempotent for an identical repeat (a re-delivered webhook, a manual
// double-entry). A conflicting repeat fails closed: two different SHAs
// claiming to be the same node's integration means reality disagrees with
// the record and a human should look, not the newer write winning.
func RecordIntegration(gdb *gorm.DB, epicID, nodeKey, sha, targetRef string) error {
	if sha == "" {
		return fmt.Errorf("epics: integration receipt needs the merged SHA")
	}
	return gdb.Transaction(func(tx *gorm.DB) error {
		var e db.Epic
		if err := tx.Where("id = ?", epicID).First(&e).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// A merge is a fact whenever it happens; only a finished run refuses.
		if e.State != "running" && e.State != "paused" && e.State != "needs-attention" {
			return ErrWrongState
		}
		var node db.EpicNode
		err := tx.Where("epic_id = ? AND revision = ? AND node_key = ?",
			epicID, e.ApprovedRevision, nodeKey).First(&node).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("epics: node %q is not in the approved plan", nodeKey)
			}
			return err
		}
		if node.IntegratedSHA != "" {
			if node.IntegratedSHA == sha && node.IntegratedRef == targetRef {
				return nil // the same receipt twice is one receipt
			}
			return ErrIntegrationConflict
		}
		now := timeNow()
		if err := tx.Model(&db.EpicNode{}).Where("id = ?", node.ID).Updates(map[string]any{
			"integrated_sha": sha, "integrated_ref": targetRef, "integrated_at": &now,
		}).Error; err != nil {
			return err
		}
		plan, _, err := LoadPlan(tx, epicID, e.ApprovedRevision)
		if err != nil {
			return err
		}
		if err := reconcileReadiness(tx, &e, plan); err != nil {
			return err
		}
		// Every node integrated: for a feature-branch delivery the run now
		// waits on the final review; base-branch delivery machinery arrives
		// with the tester milestone. Either way the run leaves "running".
		all := true
		for _, n := range plan.Nodes {
			if n.Key == nodeKey {
				continue // just written above
			}
			var m db.EpicNode
			if err := tx.Where("epic_id = ? AND revision = ? AND node_key = ?",
				epicID, e.ApprovedRevision, n.Key).First(&m).Error; err != nil {
				return err
			}
			if m.IntegratedSHA == "" {
				all = false
				break
			}
		}
		if all {
			// Trunk + flags integrated into base already: that was the delivery.
			next := "awaiting-final-review"
			if e.BranchStrategy == StrategyTrunkFlags {
				next = "completed"
			}
			if err := tx.Model(&db.Epic{}).
				Where("id = ? AND state IN ?", epicID, []string{"running", "paused"}).
				Update("state", next).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
