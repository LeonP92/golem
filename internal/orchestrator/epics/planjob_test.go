package epics

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/epic"
	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestRequestPlanJobAllowsOneActiveJob(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	if _, _, err := RequestPlanJob(gdb, e.ID); err != nil {
		t.Fatalf("RequestPlanJob: %v", err)
	}
	if _, _, err := RequestPlanJob(gdb, e.ID); !errors.Is(err, ErrPlanJobActive) {
		t.Fatalf("second active job: err = %v, want ErrPlanJobActive", err)
	}
}

func TestRequestPlanJobRejectsStartedEpic(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	gdb.Model(e).Update("state", StateRunning)
	if _, _, err := RequestPlanJob(gdb, e.ID); !errors.Is(err, ErrNotEditable) {
		t.Fatalf("err = %v, want ErrNotEditable", err)
	}
}

func TestPlanJobClaimCompletesAndRejectsStrangers(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	_, job, err := RequestPlanJob(gdb, e.ID)
	if err != nil {
		t.Fatalf("RequestPlanJob: %v", err)
	}
	now := time.Now().UTC()

	jobs, err := AvailablePlanJobs(gdb, "https://github.com/org/repo", now)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("AvailablePlanJobs = %v, %v", jobs, err)
	}

	payload, err := ClaimPlanJob(gdb, job.ID, 7, now)
	if err != nil {
		t.Fatalf("ClaimPlanJob: %v", err)
	}
	if payload.BranchTarget != "epic/"+e.ID || payload.SpecMarkdown == "" || payload.RepoRemote == "" {
		t.Errorf("payload wrong: %+v", payload)
	}
	// A fresh job is no longer claimable.
	if _, err := ClaimPlanJob(gdb, job.ID, 8, now); !errors.Is(err, ErrPlanJobNotClaimable) {
		t.Fatalf("re-claim: err = %v, want ErrPlanJobNotClaimable", err)
	}
	// A stranger cannot report progress or complete.
	if err := PostPlanJobProgress(gdb, job.ID, 8, "x", false, now); !errors.Is(err, ErrPlanJobNotOwned) {
		t.Fatalf("stranger progress: err = %v", err)
	}
	if err := CompletePlanJob(gdb, job.ID, 8, "{}", ""); !errors.Is(err, ErrPlanJobNotOwned) {
		t.Fatalf("stranger complete: err = %v", err)
	}
	// The owner completes with a valid draft; branch targets are filled.
	plan := validPlan()
	data, _ := json.Marshal(plan)
	if err := CompletePlanJob(gdb, job.ID, 7, string(data), ""); err != nil {
		t.Fatalf("CompletePlanJob: %v", err)
	}
	done, err := LatestPlanJob(gdb, e.ID)
	if err != nil || done == nil {
		t.Fatalf("LatestPlanJob: %v", err)
	}
	if done.Status != PlanJobDone || done.DraftJSON == "" {
		t.Fatalf("job = %+v", done)
	}
	var stored epic.Plan
	if err := json.Unmarshal([]byte(done.DraftJSON), &stored); err != nil {
		t.Fatalf("stored draft: %v", err)
	}
	for _, n := range stored.Nodes {
		if n.BranchTarget != "epic/"+e.ID {
			t.Errorf("node %q branch target = %q, want filled", n.Key, n.BranchTarget)
		}
	}
}

func TestCompletePlanJobRejectsInvalidDraft(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	_, job, _ := RequestPlanJob(gdb, e.ID)
	now := time.Now().UTC()
	if _, err := ClaimPlanJob(gdb, job.ID, 7, now); err != nil {
		t.Fatalf("ClaimPlanJob: %v", err)
	}
	// A draft the validator rejects must fail the job, never be stored.
	if err := CompletePlanJob(gdb, job.ID, 7, `{"requirements":[],"nodes":[],"edges":[]}`, ""); err != nil {
		t.Fatalf("CompletePlanJob: %v", err)
	}
	done, _ := LatestPlanJob(gdb, e.ID)
	if done.Status != PlanJobFailed || done.Err == "" || done.DraftJSON != "" {
		t.Fatalf("job = %+v", done)
	}
}

func TestPlanJobStaleLeaseIsReclaimedOnce(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	_, job, _ := RequestPlanJob(gdb, e.ID)
	t0 := time.Now().UTC()
	if _, err := ClaimPlanJob(gdb, job.ID, 7, t0); err != nil {
		t.Fatalf("ClaimPlanJob: %v", err)
	}
	// Two sessions' worth of attempts, then the shem dies.
	if err := PostPlanJobProgress(gdb, job.ID, 7, "s1", true, t0); err != nil {
		t.Fatalf("progress 1: %v", err)
	}
	if err := PostPlanJobProgress(gdb, job.ID, 7, "s2", true, t0); err != nil {
		t.Fatalf("progress 2: %v", err)
	}
	later := t0.Add(PlanJobStaleAfter + time.Minute)
	jobs, err := AvailablePlanJobs(gdb, "https://github.com/org/repo", later)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("stale job should be available: %v, %v", jobs, err)
	}
	// The reclaim fails the job: the attempt budget is gone.
	if _, err := ClaimPlanJob(gdb, job.ID, 8, later); !errors.Is(err, ErrPlanJobNotClaimable) {
		t.Fatalf("exhausted reclaim: err = %v", err)
	}
	done, _ := LatestPlanJob(gdb, e.ID)
	if done.Status != PlanJobFailed || done.Err == "" {
		t.Fatalf("job = %+v, want failed with a reason", done)
	}
}

func TestHandWrittenPlanFlowUntouchedByPlanner(t *testing.T) {
	// The manual path must work with no plan job ever created: add a
	// revision, approve it, start the run — exactly as before planner jobs
	// existed.
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	if _, err := AddRevision(gdb, e.ID, validPlan(), ""); err != nil {
		t.Fatalf("AddRevision: %v", err)
	}
	var rev db.EpicPlanRevision
	if err := gdb.Where("epic_id = ?", e.ID).First(&rev).Error; err != nil {
		t.Fatalf("load revision: %v", err)
	}
	if err := Approve(gdb, e.ID, 1, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := Start(gdb, e.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if job, err := LatestPlanJob(gdb, e.ID); err != nil || job != nil {
		t.Fatalf("no plan job should exist: %v, %v", job, err)
	}
}

func TestCompletePlanJobAutoStartWhenOptedIn(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e, err := Create(gdb, CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Auto", SpecMarkdown: "# Auto",
		AutoStartGeneratedPlan: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, job, _ := RequestPlanJob(gdb, e.ID)
	now := time.Now().UTC()
	if _, err := ClaimPlanJob(gdb, job.ID, 7, now); err != nil {
		t.Fatalf("ClaimPlanJob: %v", err)
	}
	data, _ := json.Marshal(validPlan())
	if err := CompletePlanJob(gdb, job.ID, 7, string(data), ""); err != nil {
		t.Fatalf("CompletePlanJob: %v", err)
	}
	var after db.Epic
	if err := gdb.First(&after, "id = ?", e.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != StateRunning {
		t.Fatalf("state = %q, want running (revision saved, approved, started)", after.State)
	}
	var rev db.EpicPlanRevision
	if err := gdb.Where("epic_id = ?", e.ID).First(&rev).Error; err != nil {
		t.Fatal(err)
	}
	if rev.ApprovedBy != "auto:plan-generation" || rev.ApprovedAt == nil {
		t.Errorf("approval audit wrong: %+v", rev)
	}
	if after.ApprovedPlanHash != rev.PlanHash {
		t.Error("the hash binding must be recorded exactly as a human approval's")
	}
	var children int64
	gdb.Model(&db.Ticket{}).Where("epic_id = ?", e.ID).Count(&children)
	if children != int64(len(validPlan().Nodes)) {
		t.Errorf("children = %d, want %d", children, len(validPlan().Nodes))
	}
}

func TestCompletePlanJobAutoStartOffByDefault(t *testing.T) {
	gdb, _ := db.Open(":memory:")
	e := createEpic(t, gdb)
	if e.AutoStartGeneratedPlan {
		t.Fatal("must default to off")
	}
	_, job, _ := RequestPlanJob(gdb, e.ID)
	now := time.Now().UTC()
	if _, err := ClaimPlanJob(gdb, job.ID, 7, now); err != nil {
		t.Fatalf("ClaimPlanJob: %v", err)
	}
	data, _ := json.Marshal(validPlan())
	if err := CompletePlanJob(gdb, job.ID, 7, string(data), ""); err != nil {
		t.Fatalf("CompletePlanJob: %v", err)
	}
	var after db.Epic
	if err := gdb.First(&after, "id = ?", e.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != StateDraft || after.CurrentRevision != 0 {
		t.Fatalf("default path must stay manual: state = %q revisions = %d", after.State, after.CurrentRevision)
	}
}
