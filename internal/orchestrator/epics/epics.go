// Package epics is the orchestrator side of Epic Runs: creating epics,
// immutable plan revisions bound to approvals, materializing children as
// ordinary tickets, integration receipts and the merge lane.
package epics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
)

const (
	StrategyEpicBranch = "epic-branch"
	StrategyTrunkFlags = "trunk-flags"

	TargetFeatureBranch = "feature-branch"
	TargetBaseBranch    = "base-branch"
)

// Run states.
const (
	StateDraft               = "draft"
	StateAwaitingApproval    = "awaiting-approval"
	StateRunning             = "running"
	StatePaused              = "paused"
	StateAwaitingFinalReview = "awaiting-final-review"
	StateCompleted           = "completed"
)

// States lists the run states in display order.
var States = []string{StateDraft, StateAwaitingApproval, StateRunning, StatePaused,
	StateAwaitingFinalReview, StateCompleted}

var (
	ErrNotFound        = errors.New("epic not found")
	ErrNotEditable     = errors.New("epic is no longer editable once started")
	ErrHashMismatch    = errors.New("plan hash does not match the revision")
	ErrRevisionMissing = errors.New("plan revision not found")
	ErrInvalid         = errors.New("epics: invalid input")
	ErrNotCurrent      = errors.New("only the current revision of a run awaiting approval can be approved")
)

type CreateParams struct {
	RepoRemote        string
	Title             string
	SpecMarkdown      string
	BaseBranch        string
	BranchStrategy    string
	DeliveryTarget    string
	AutoMergeChildren bool
	AutoApprovePhases bool
	CreatedByUserID   *uint
}

// Create stores a new epic in draft. Strategy and delivery target never
// change afterwards, so invalid combinations are rejected here.
func Create(gdb *gorm.DB, p CreateParams) (*db.Epic, error) {
	if p.RepoRemote == "" || p.Title == "" || p.SpecMarkdown == "" {
		return nil, fmt.Errorf("%w: repo, title and spec are required", ErrInvalid)
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
	if p.BranchStrategy != StrategyEpicBranch && p.BranchStrategy != StrategyTrunkFlags {
		return nil, fmt.Errorf("%w: unknown branch strategy %q", ErrInvalid, p.BranchStrategy)
	}
	if p.DeliveryTarget != TargetFeatureBranch && p.DeliveryTarget != TargetBaseBranch {
		return nil, fmt.Errorf("%w: unknown delivery target %q", ErrInvalid, p.DeliveryTarget)
	}
	if p.BranchStrategy == StrategyTrunkFlags && p.DeliveryTarget == TargetFeatureBranch {
		return nil, fmt.Errorf("%w: trunk-flags integrates to base and has no feature branch; delivery target must be base-branch", ErrInvalid)
	}

	sum := sha256.Sum256([]byte(p.SpecMarkdown))
	e := &db.Epic{
		ID:                uuid.NewString(),
		RepoRemote:        p.RepoRemote,
		Title:             p.Title,
		SpecMarkdown:      p.SpecMarkdown,
		SpecHash:          hex.EncodeToString(sum[:]),
		BaseBranch:        p.BaseBranch,
		BranchStrategy:    p.BranchStrategy,
		DeliveryTarget:    p.DeliveryTarget,
		AutoMergeChildren: p.AutoMergeChildren,
		TesterRequired:    p.AutoMergeChildren,
		AutoApprovePhases: p.AutoApprovePhases,
		State:             StateDraft,
		CreatedByUserID:   p.CreatedByUserID,
	}
	if p.BranchStrategy == StrategyEpicBranch {
		e.IntegrationRef = "epic/" + e.ID
	}
	if err := gdb.Create(e).Error; err != nil {
		return nil, fmt.Errorf("create epic: %w", err)
	}
	return e, nil
}

// TargetRef is where children merge: the integration ref, or base under
// trunk-flags.
func TargetRef(e *db.Epic) string {
	if e.IntegrationRef != "" {
		return e.IntegrationRef
	}
	return e.BaseBranch
}

// List returns epics newest first, optionally filtered.
func List(gdb *gorm.DB, repo, state string) ([]db.Epic, error) {
	var list []db.Epic
	q := gdb.Order("created_at desc")
	if repo != "" {
		q = q.Where("repo_remote = ?", urlnorm.Normalize(repo))
	}
	if state != "" {
		q = q.Where("state = ?", state)
	}
	return list, q.Find(&list).Error
}

func load(gdb *gorm.DB, id string) (*db.Epic, error) {
	var e db.Epic
	if err := gdb.First(&e, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &e, nil
}

// AddRevision validates a plan and stores it as the next immutable
// revision, clearing any previous approval. A node's empty branch_target
// is filled with the run's target ref; any other value is rejected.
func AddRevision(gdb *gorm.DB, epicID string, plan epic.Plan, repoSHA string) (*db.EpicPlanRevision, error) {
	var rev db.EpicPlanRevision
	err := gdb.Transaction(func(tx *gorm.DB) error {
		e, err := load(tx, epicID)
		if err != nil {
			return err
		}
		if e.State != StateDraft && e.State != StateAwaitingApproval {
			return ErrNotEditable
		}
		target := TargetRef(e)
		var errs []error
		for i, n := range plan.Nodes {
			switch n.BranchTarget {
			case "":
				plan.Nodes[i].BranchTarget = target
			case target:
			default:
				errs = append(errs, fmt.Errorf("node %q targets %q; this run integrates into %q", n.Key, n.BranchTarget, target))
			}
		}
		if errs = append(errs, epic.Validate(plan)...); len(errs) > 0 {
			return fmt.Errorf("%w: plan: %w", ErrInvalid, errors.Join(errs...))
		}
		canonical, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(canonical)
		rev = db.EpicPlanRevision{
			EpicID:   epicID,
			Revision: e.CurrentRevision + 1,
			SpecHash: e.SpecHash,
			RepoSHA:  repoSHA,
			PlanJSON: string(canonical),
			PlanHash: hex.EncodeToString(sum[:]),
		}
		if err := tx.Create(&rev).Error; err != nil {
			return err
		}
		return tx.Model(e).Updates(map[string]any{
			"current_revision":   rev.Revision,
			"approved_revision":  0,
			"approved_plan_hash": "",
			"state":              StateAwaitingApproval,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

// Approve binds approval to an exact revision and its content hash, so a
// re-save can never carry an approval onto content the human did not see.
func Approve(gdb *gorm.DB, epicID string, revision int, planHash, actor string) error {
	return gdb.Transaction(func(tx *gorm.DB) error {
		e, err := load(tx, epicID)
		if err != nil {
			return err
		}
		if e.State != StateAwaitingApproval || revision != e.CurrentRevision {
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
		now := time.Now()
		if err := tx.Model(&rev).Updates(map[string]any{"approved_by": actor, "approved_at": &now}).Error; err != nil {
			return err
		}
		return tx.Model(e).Updates(map[string]any{
			"approved_revision":  revision,
			"approved_plan_hash": planHash,
		}).Error
	})
}

// LoadPlan returns the stored plan for a revision.
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
