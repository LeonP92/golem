// Package epics is the orchestrator-side service for Epic Runs: creating
// epics, storing immutable plan revisions, and binding approvals to exact
// plan content. Execution (materializing tickets, scheduling, merging) is
// a later milestone; this package guarantees that what runs later is
// exactly what a human approved.
package epics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// Branch strategies and delivery targets, from the approved design.
const (
	StrategyEpicBranch = "epic-branch"
	StrategyTrunkFlags = "trunk-flags"

	TargetFeatureBranch = "feature-branch"
	TargetBaseBranch    = "base-branch"
)

var (
	ErrNotFound        = errors.New("epic not found")
	ErrNotEditable     = errors.New("epic is no longer editable; mid-run plan deltas are a later milestone")
	ErrHashMismatch    = errors.New("plan hash does not match the revision — approval must bind to the exact plan reviewed")
	ErrRevisionMissing = errors.New("plan revision not found")
	ErrNotCurrent      = errors.New("only the current revision of a run awaiting approval can be approved")
)

type CreateParams struct {
	RepoRemote     string
	Title          string
	SpecMarkdown   string
	BaseBranch     string
	BranchStrategy string
	DeliveryTarget string
	// AutoMergeChildren and AutoMergeFinal are the two independent
	// auto-merge toggles. Final is only meaningful for a base-branch
	// delivery target: a feature-branch handoff is always the human's
	// merge. Any enabled auto-merge forces TesterRequired on — an
	// auto-merge without a tester verdict is precisely the failure the
	// tester gate exists to prevent.
	AutoMergeChildren bool
	AutoMergeFinal    bool
	// AutoApprovePhases makes children skip the human spec/plan approval;
	// the snapshot applies at Start and each child keeps its own
	// ManualApproval as the override.
	AutoApprovePhases bool
	CreatedByUserID   *uint
}

// Create stores a new epic in draft. Strategy and delivery target are set
// here and never change mid-run; an invalid combination is rejected now
// rather than surfacing as a surprise at merge time.
func Create(gdb *gorm.DB, p CreateParams) (*db.Epic, error) {
	if p.RepoRemote == "" || p.Title == "" || p.SpecMarkdown == "" {
		return nil, fmt.Errorf("repo, title and spec are required")
	}
	if p.BaseBranch == "" {
		p.BaseBranch = "main"
	}
	if p.BranchStrategy == "" {
		p.BranchStrategy = StrategyEpicBranch
	}
	if p.DeliveryTarget == "" {
		p.DeliveryTarget = TargetFeatureBranch
	}
	switch p.BranchStrategy {
	case StrategyEpicBranch:
	case StrategyTrunkFlags:
		// Trunk integrates slices directly to base; there is no assembled
		// feature branch to hand off, so a feature-branch delivery target
		// contradicts the strategy.
		if p.DeliveryTarget == TargetFeatureBranch {
			return nil, fmt.Errorf("trunk-flags integrates to base and has no final feature-branch handoff; delivery target must be base-branch")
		}
	default:
		return nil, fmt.Errorf("unknown branch strategy %q", p.BranchStrategy)
	}
	if p.DeliveryTarget != TargetFeatureBranch && p.DeliveryTarget != TargetBaseBranch {
		return nil, fmt.Errorf("unknown delivery target %q", p.DeliveryTarget)
	}
	if p.AutoMergeFinal && p.DeliveryTarget != TargetBaseBranch {
		return nil, fmt.Errorf("final auto-merge requires the base-branch delivery target; a feature-branch handoff always stays human-merged")
	}

	sum := sha256.Sum256([]byte(p.SpecMarkdown))
	e := &db.Epic{
		ID:              uuid.NewString(),
		RepoRemote:      p.RepoRemote,
		Title:           p.Title,
		SpecMarkdown:    p.SpecMarkdown,
		SpecHash:        hex.EncodeToString(sum[:]),
		BaseBranch:      p.BaseBranch,
		BranchStrategy:  p.BranchStrategy,
		DeliveryTarget:  p.DeliveryTarget,
		State:           "draft",
		CreatedByUserID: p.CreatedByUserID,
	}
	e.AutoMergeChildren = p.AutoMergeChildren
	e.AutoMergeFinal = p.AutoMergeFinal
	e.AutoApprovePhases = p.AutoApprovePhases
	e.TesterRequired = p.AutoMergeChildren || p.AutoMergeFinal

	if p.BranchStrategy == StrategyEpicBranch {
		e.IntegrationRef = "epic/" + e.ID
	}
	if err := gdb.Create(e).Error; err != nil {
		return nil, fmt.Errorf("create epic: %w", err)
	}
	return e, nil
}

// AddRevision validates a candidate plan and stores it as the epic's next
// immutable revision, with its node and edge snapshots, in one transaction.
// Any previous approval is cleared: editing content after approval must
// never inherit the old approval.
func AddRevision(gdb *gorm.DB, epicID string, plan epic.Plan, repoSHA string) (*db.EpicPlanRevision, error) {
	if errs := epic.Validate(plan); len(errs) > 0 {
		return nil, fmt.Errorf("plan is invalid: %v", errors.Join(errs...))
	}
	hash, err := epic.Hash(plan)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}

	var rev db.EpicPlanRevision
	err = gdb.Transaction(func(tx *gorm.DB) error {
		var e db.Epic
		if err := tx.Where("id = ?", epicID).First(&e).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// v1 allows editing before execution only. Approved mid-run deltas
		// arrive as their own milestone; silently mutating live approved
		// scope is exactly what the design forbids.
		if e.State != "draft" && e.State != "planning" && e.State != "awaiting-approval" {
			return ErrNotEditable
		}

		rev = db.EpicPlanRevision{
			EpicID:   epicID,
			Revision: e.CurrentRevision + 1,
			SpecHash: e.SpecHash,
			RepoSHA:  repoSHA,
			PlanJSON: string(canonical),
			PlanHash: hash,
		}
		if err := tx.Create(&rev).Error; err != nil {
			return err
		}
		for _, n := range plan.Nodes {
			reqs, _ := json.Marshal(n.RequirementIDs)
			node := db.EpicNode{
				EpicID: epicID, Revision: rev.Revision, NodeKey: n.Key,
				Title: n.Title, RequirementIDs: string(reqs), BranchTarget: n.BranchTarget,
			}
			if err := tx.Create(&node).Error; err != nil {
				return err
			}
		}
		for _, ed := range plan.Edges {
			edge := db.EpicEdge{
				EpicID: epicID, Revision: rev.Revision,
				Predecessor: ed.Predecessor, Successor: ed.Successor, Reason: ed.Reason,
			}
			if err := tx.Create(&edge).Error; err != nil {
				return err
			}
		}
		return tx.Model(&e).Updates(map[string]any{
			"current_revision":   rev.Revision,
			"approved_revision":  0,
			"approved_plan_hash": "",
			"state":              "awaiting-approval",
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

// Approve binds a human's approval to an exact revision AND its content
// hash. Both must match what is stored: approving "the plan" without the
// hash would let a quiet re-save carry an approval onto content the human
// never saw.
func Approve(gdb *gorm.DB, epicID string, revision int, planHash, actor string) error {
	return gdb.Transaction(func(tx *gorm.DB) error {
		var e db.Epic
		if err := tx.Where("id = ?", epicID).First(&e).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		// Only the current revision of a run not yet started: approving a
		// replaced revision, or re-approving a live run, wedges its children.
		if e.State != "awaiting-approval" || revision != e.CurrentRevision {
			return ErrNotCurrent
		}
		var rev db.EpicPlanRevision
		if err := tx.Where("epic_id = ? AND revision = ?", epicID, revision).First(&rev).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrRevisionMissing
			}
			return err
		}
		if rev.PlanHash != planHash {
			return ErrHashMismatch
		}
		now := timeNow()
		if err := tx.Model(&rev).Updates(map[string]any{
			"approved_by": actor, "approved_at": &now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&e).Updates(map[string]any{
			"approved_revision":  revision,
			"approved_plan_hash": planHash,
		}).Error
	})
}

// LoadPlan returns the stored plan for a revision. The DAG view and the
// list view render from this same record, so they cannot drift apart.
func LoadPlan(gdb *gorm.DB, epicID string, revision int) (*epic.Plan, *db.EpicPlanRevision, error) {
	var rev db.EpicPlanRevision
	if err := gdb.Where("epic_id = ? AND revision = ?", epicID, revision).First(&rev).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrRevisionMissing
		}
		return nil, nil, err
	}
	var plan epic.Plan
	if err := json.Unmarshal([]byte(rev.PlanJSON), &plan); err != nil {
		return nil, nil, fmt.Errorf("stored plan revision %d does not decode: %w", revision, err)
	}
	return &plan, &rev, nil
}
