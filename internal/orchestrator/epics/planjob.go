// Plan-generation jobs: a durable record that a shem claims with a lease,
// runs one planning agent session for, and completes with a draft plan or
// an error. The draft becomes a plan revision only when a human saves it
// through AddRevision, unless the epic opted into AutoStartGeneratedPlan;
// the human approval gate holds by default and is untouched by how the
// JSON was produced.
package epics

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

// Plan job statuses.
const (
	PlanJobQueued  = "queued"
	PlanJobRunning = "running"
	PlanJobDone    = "done"
	PlanJobFailed  = "failed"
)

// PlanJobStaleAfter is the lease window: a running job whose claim is older
// than this is treated as abandoned and may be claimed again. The claiming
// shem refreshes the claim with every progress post, so this only fires
// when the shem is actually gone.
const PlanJobStaleAfter = 5 * time.Minute

// PlanJobMaxAttempts bounds agent sessions per job: the first try plus one
// repair pass seeded with the validator's errors.
const PlanJobMaxAttempts = 2

var (
	ErrPlanJobActive       = errors.New("a plan job is already active for this epic")
	ErrPlanJobNotFound     = errors.New("plan job not found")
	ErrPlanJobNotClaimable = errors.New("plan job is not claimable")
	ErrPlanJobNotOwned     = errors.New("plan job is claimed by another shem")
)

// PlanJobPayload is everything a shem needs to run the planning session.
type PlanJobPayload struct {
	Job          db.EpicPlanJob `json:"job"`
	EpicTitle    string         `json:"epic_title"`
	SpecMarkdown string         `json:"spec_markdown"`
	RepoRemote   string         `json:"repo_remote"`
	// BranchTarget is the ref every plan node must name — the run's
	// integration ref or base branch.
	BranchTarget string `json:"branch_target"`
}

// RequestPlanJob queues a plan-generation job for an epic that is still
// being planned (draft or awaiting approval) and returns the epic with it,
// so the caller can broadcast to its repo without a second query. One
// active job per epic; the partial unique index is the backstop for
// concurrent requests.
func RequestPlanJob(gdb *gorm.DB, epicID string) (*db.Epic, *db.EpicPlanJob, error) {
	e, err := load(gdb, epicID)
	if err != nil {
		return nil, nil, err
	}
	if e.State != StateDraft && e.State != StateAwaitingApproval {
		return nil, nil, ErrNotEditable
	}
	job := &db.EpicPlanJob{ID: uuid.NewString(), EpicID: epicID, Status: PlanJobQueued}
	if err := gdb.Create(job).Error; err != nil {
		if isDuplicateKey(err) {
			return nil, nil, ErrPlanJobActive
		}
		return nil, nil, err
	}
	return e, job, nil
}

// LatestPlanJob returns the newest job for an epic, for the detail page.
func LatestPlanJob(gdb *gorm.DB, epicID string) (*db.EpicPlanJob, error) {
	var job db.EpicPlanJob
	err := gdb.Where("epic_id = ?", epicID).Order("created_at desc").First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &job, nil
}

// AvailablePlanJobs lists jobs a shem on repo may claim: queued jobs, plus
// running jobs whose lease went stale (the shem died mid-session).
func AvailablePlanJobs(gdb *gorm.DB, repo string, now time.Time) ([]db.EpicPlanJob, error) {
	var jobs []db.EpicPlanJob
	stale := now.Add(-PlanJobStaleAfter)
	q := gdb.Joins("JOIN epics ON epics.id = epic_plan_jobs.epic_id").
		Where("epics.repo_remote = ? AND epics.state IN ?", repo, []string{StateDraft, StateAwaitingApproval}).
		Where("epic_plan_jobs.status = ? OR (epic_plan_jobs.status = ? AND epic_plan_jobs.claimed_at < ?)",
			PlanJobQueued, PlanJobRunning, stale).
		Order("epic_plan_jobs.created_at")
	return jobs, q.Find(&jobs).Error
}

// ClaimPlanJob takes the lease on a claimable job. The WHERE clause is the
// compare-and-swap: exactly one claimant wins, and a reclaimed job keeps
// its attempt count so the repair bound survives a shem restart.
func ClaimPlanJob(gdb *gorm.DB, jobID string, shemID uint, now time.Time) (*PlanJobPayload, error) {
	stale := now.Add(-PlanJobStaleAfter)
	res := gdb.Model(&db.EpicPlanJob{}).
		Where("id = ? AND (status = ? OR (status = ? AND claimed_at < ?))",
			jobID, PlanJobQueued, PlanJobRunning, stale).
		Updates(map[string]any{
			"status":     PlanJobRunning,
			"shem_id":    shemID,
			"claimed_at": now,
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrPlanJobNotClaimable
	}
	payload, err := loadPlanJobPayload(gdb, jobID)
	if err != nil {
		return nil, err
	}
	// A reclaimed job keeps its attempt count; once the budget is gone the
	// job fails instead of bouncing between shems forever.
	if payload.Job.Attempt >= PlanJobMaxAttempts {
		uerr := gdb.Model(&db.EpicPlanJob{}).Where("id = ?", jobID).
			Updates(map[string]any{"status": PlanJobFailed,
				"error": fmt.Sprintf("abandoned after %d sessions without a valid plan", PlanJobMaxAttempts)}).Error
		if uerr != nil {
			return nil, uerr
		}
		return nil, ErrPlanJobNotClaimable
	}
	return payload, nil
}

func loadPlanJobPayload(gdb *gorm.DB, jobID string) (*PlanJobPayload, error) {
	var job db.EpicPlanJob
	if err := gdb.First(&job, "id = ?", jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPlanJobNotFound
		}
		return nil, err
	}
	e, err := load(gdb, job.EpicID)
	if err != nil {
		return nil, err
	}
	return &PlanJobPayload{
		Job:          job,
		EpicTitle:    e.Title,
		SpecMarkdown: e.SpecMarkdown,
		RepoRemote:   e.RepoRemote,
		BranchTarget: TargetRef(e),
	}, nil
}

// PostPlanJobProgress refreshes the lease and stores the session log tail.
// The attempt increment happens here because the shem posts progress once
// per session start; reclaiming therefore consumes the attempt already.
func PostPlanJobProgress(gdb *gorm.DB, jobID string, shemID uint, tail string, newAttempt bool, now time.Time) error {
	updates := map[string]any{"claimed_at": now}
	if tail != "" {
		updates["log_tail"] = tail
	}
	q := gdb.Model(&db.EpicPlanJob{}).
		Where("id = ? AND status = ? AND shem_id = ?", jobID, PlanJobRunning, shemID)
	if newAttempt {
		q = q.Where("attempt < ?", PlanJobMaxAttempts)
		updates["attempt"] = gorm.Expr("attempt + 1")
	}
	res := q.Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPlanJobNotOwned
	}
	return nil
}

// CompletePlanJob stores the outcome. A non-empty runErr fails the job;
// otherwise the draft is validated exactly as AddRevision would validate a
// hand-pasted plan (branch targets applied, then the full structural
// rules) before it is stored — the shem's own validation is never trusted.
func CompletePlanJob(gdb *gorm.DB, jobID string, shemID uint, draftJSON, runErr string) error {
	var job db.EpicPlanJob
	if err := gdb.First(&job, "id = ?", jobID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPlanJobNotFound
		}
		return err
	}
	if job.Status != PlanJobRunning || job.ShemID == nil || *job.ShemID != shemID {
		return ErrPlanJobNotOwned
	}
	e, err := load(gdb, job.EpicID)
	if err != nil {
		return err
	}
	updates := map[string]any{}
	canonical := ""
	if runErr != "" {
		updates["status"], updates["error"] = PlanJobFailed, runErr
	} else if c, errs := epic.CanonicalPlan([]byte(draftJSON), TargetRef(e)); len(errs) > 0 {
		updates["status"], updates["error"] = PlanJobFailed, fmt.Errorf("generated plan is invalid: %w", errors.Join(errs...)).Error()
	} else {
		canonical = c
		updates["status"], updates["draft_json"] = PlanJobDone, canonical
	}
	if err := gdb.Model(&job).Updates(updates).Error; err != nil {
		return err
	}
	// Opt-in automation: a valid generated draft flows straight into the
	// ordinary revision -> approve -> start chain. Every step is the same
	// exported function a human click would call, so the revision hash is
	// recorded and bound exactly as a manual approval's; the actor names
	// the automation, not a human.
	if updates["status"] == PlanJobDone && e.AutoStartGeneratedPlan {
		if err := autoStartGeneratedPlan(gdb, e.ID, canonical); err != nil {
			return gdb.Model(&job).Update("error",
				"plan generated, but auto-start failed: "+err.Error()).Error
		}
	}
	return nil
}

// autoStartGeneratedPlan runs the standard revision/approve/start chain
// for a validated generated draft. Any refusal (the epic moved on while
// the job ran, an invalid plan — impossible here but checked again, a
// start conflict) is returned for the job record; the draft itself stays
// stored and visible.
func autoStartGeneratedPlan(gdb *gorm.DB, epicID, draftJSON string) error {
	var plan epic.Plan
	if err := json.Unmarshal([]byte(draftJSON), &plan); err != nil {
		return err
	}
	rev, err := AddRevision(gdb, epicID, plan, "")
	if err != nil {
		return err
	}
	if err := Approve(gdb, epicID, rev.Revision, rev.PlanHash, "auto:plan-generation"); err != nil {
		return err
	}
	_, err = Start(gdb, epicID)
	return err
}

// isDuplicateKey reports whether err is a unique-constraint violation: the
// translated GORM error, or the SQLite/Postgres messages that reach us
// untranslated.
func isDuplicateKey(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate key") ||
		strings.Contains(msg, "unique_violation")
}
