package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/epics"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/shem/worker"
	"gorm.io/gorm"
	"time"
)

// planWriterAdapter stands in for a vendor CLI: the planning "session"
// reads the prompt, extracts the plan path and branch target, and writes a
// valid plan — the same contract a real backend fulfills.
type planWriterAdapter struct {
	*agentrunner.Mock
	seen []string
}

var (
	planPathRe  = regexp.MustCompile(`Write the plan to: (\S+)`)
	branchRefRe = regexp.MustCompile(`Branch target: (\S+)`)
)

func (a *planWriterAdapter) RunPhase(_ context.Context, dir, prompt string, out io.Writer, _ string) error {
	a.seen = append(a.seen, prompt)
	path := planPathRe.FindStringSubmatch(prompt)[1]
	ref := branchRefRe.FindStringSubmatch(prompt)[1]
	plan := `{"requirements":[{"id":"R1","text":"generated"}],"nodes":[{"key":"T1","title":"generated node","acceptance_commands":["go test ./..."],"requirement_ids":["R1"],"branch_target":"` + ref + `"}],"edges":[]}`
	if err := os.WriteFile(path, []byte(plan), 0o600); err != nil {
		return err
	}
	_, err := io.WriteString(out, "session output: plan written\n")
	return err
}

func newSessionCookie(t *testing.T, h *api.Handlers, userID uint) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(h.DB, rec, userID, false); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

// TestEpicPlanJobEndToEnd drives the whole feature over real HTTP with a
// real (in-memory) database: an operator queues plan generation, a shem
// polls, claims the job, runs its "agent", and posts the draft; the page's
// status endpoint then shows a validated draft, which the operator saves
// as a revision and approves — the generated JSON flows through exactly
// the same gate as a hand-pasted plan.
func TestEpicPlanJobEndToEnd(t *testing.T) {
	gdb := openTestDB(t)
	seedTestShem(t, gdb, "shem-1", "key-1")

	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterEpicRoutes(mux)
	h.RegisterEpicPlanJobRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	user := db.User{Username: "leon", Role: "admin"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	cookie := newSessionCookie(t, h, user.ID)

	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Workspaces",
		SpecMarkdown: "# Workspaces\n\nAdd workspaces.",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}

	// 1. The operator queues generation from the page.
	req, _ := http.NewRequest("POST", server.URL+"/api/epics/"+e.ID+"/plan/generate", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(cookie.Value))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("generate: %d %s", resp.StatusCode, body)
	}
	resp.Body.Close()

	// 2. The shem polls for work, as its poll loop does every 10s.
	c := client.New(server.URL, "key-1", "shem-1")
	job, err := c.GetAvailablePlanJob("https://github.com/org/repo")
	if err != nil || job == nil {
		t.Fatalf("GetAvailablePlanJob: %v, %v", job, err)
	}

	// 3. The shem runs the job with its agent against the local checkout.
	repoPath := t.TempDir()
	adapter := &planWriterAdapter{Mock: agentrunner.NewMock()}
	w := worker.New(&config.Config{
		Repos: []config.RepoConfig{{Path: repoPath, Remote: "https://github.com/org/repo", NormalizedRemote: "https://github.com/org/repo"}},
	}, c, nil, &worker.Agent{Adapter: adapter, Catalog: adapter.DefaultCatalog()})
	// The ws push is what a live shem reacts to; HandleMessage dispatches
	// the job on a goroutine exactly as it does for tickets.
	w.HandleMessage(ws.WSMessage{Type: "epic_plan", JobID: &job.ID})
	waitForPlanJob(t, gdb, e.ID, epics.PlanJobDone)

	// 4. The page's status endpoint shows the validated draft.
	req, _ = http.NewRequest("GET", server.URL+"/api/epics/"+e.ID+"/plan/job", nil)
	req.AddCookie(cookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var finished db.EpicPlanJob
	if err := json.NewDecoder(resp.Body).Decode(&finished); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if finished.Status != epics.PlanJobDone {
		t.Fatalf("job = %+v", finished)
	}
	if !strings.Contains(finished.DraftJSON, `"branch_target":"epic/`+e.ID+`"`) {
		t.Errorf("draft not bound to the run's target: %s", finished.DraftJSON)
	}

	// 5. The operator saves the draft as a revision and approves it — the
	// ordinary flow, unchanged.
	var plan map[string]any
	if err := json.Unmarshal([]byte(finished.DraftJSON), &plan); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(plan)
	req, _ = http.NewRequest("POST", server.URL+"/api/epics/"+e.ID+"/revisions", bytes.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", auth.CSRFTokenForSession(cookie.Value))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("save revision from generated draft: %d %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	var rev db.EpicPlanRevision
	if err := gdb.Where("epic_id = ?", e.ID).First(&rev).Error; err != nil {
		t.Fatal(err)
	}
	if err := epics.Approve(gdb, e.ID, 1, rev.PlanHash, "leon"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := epics.Start(gdb, e.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	// The prompt the agent saw carried the fenced spec and the target ref.
	if len(adapter.seen) != 1 {
		t.Fatalf("sessions = %d, want 1", len(adapter.seen))
	}
	if !strings.Contains(adapter.seen[0], "<<<EPIC_SPEC") || !strings.Contains(adapter.seen[0], "Add workspaces.") {
		t.Error("prompt missing the fenced spec")
	}

	// 6. A second epic planned entirely by hand still works with no job.
	e2, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Manual", SpecMarkdown: "# Manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	if job, _ := epics.LatestPlanJob(gdb, e2.ID); job != nil {
		t.Fatal("no plan job should exist for the hand-planned epic")
	}
}

// TestPlanJobFailureEndToEnd: a session that never writes the plan fails
// the job with a visible reason, and the epic can be re-generated or
// planned by hand afterwards.
func TestPlanJobFailureEndToEnd(t *testing.T) {
	gdb := openTestDB(t)
	seedTestShem(t, gdb, "shem-1", "key-1")
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterEpicRoutes(mux)
	h.RegisterEpicPlanJobRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "T", SpecMarkdown: "# T",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := epics.RequestPlanJob(gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	c := client.New(server.URL, "key-1", "shem-1")
	job, err := c.GetAvailablePlanJob("https://github.com/org/repo")
	if err != nil || job == nil {
		t.Fatalf("no job: %v", err)
	}
	// The adapter writes nothing: both sessions fail validation.
	w := worker.New(&config.Config{
		Repos: []config.RepoConfig{{Path: t.TempDir(), Remote: "https://github.com/org/repo", NormalizedRemote: "https://github.com/org/repo"}},
	}, c, nil, &worker.Agent{Adapter: agentrunner.NewMock()})
	w.HandleMessage(ws.WSMessage{Type: "epic_plan", JobID: &job.ID})
	finished := waitForPlanJob(t, gdb, e.ID, epics.PlanJobFailed)
	if finished.Status != epics.PlanJobFailed || !strings.Contains(finished.Err, "did not write") {
		t.Fatalf("job = %+v, want failed naming the missing file", finished)
	}
	// The failure does not wedge the epic: a new job can be requested.
	if _, _, err := epics.RequestPlanJob(gdb, e.ID); err != nil {
		t.Fatalf("re-generate after failure: %v", err)
	}
}

// TestEpicPlanJobAutoStartEndToEnd: with the opt-in flag on, a valid
// generated draft saves, approves (recorded as the automation) and starts
// the run with no human clicks.
func TestEpicPlanJobAutoStartEndToEnd(t *testing.T) {
	gdb := openTestDB(t)
	seedTestShem(t, gdb, "shem-1", "key-1")
	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterEpicRoutes(mux)
	h.RegisterEpicPlanJobRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	e, err := epics.Create(gdb, epics.CreateParams{
		RepoRemote: "https://github.com/org/repo", Title: "Auto", SpecMarkdown: "# Auto",
		AutoStartGeneratedPlan: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := epics.RequestPlanJob(gdb, e.ID); err != nil {
		t.Fatal(err)
	}
	c := client.New(server.URL, "key-1", "shem-1")
	job, err := c.GetAvailablePlanJob("https://github.com/org/repo")
	if err != nil || job == nil {
		t.Fatalf("no job: %v", err)
	}
	w := worker.New(&config.Config{
		Repos: []config.RepoConfig{{Path: t.TempDir(), Remote: "https://github.com/org/repo", NormalizedRemote: "https://github.com/org/repo"}},
	}, c, nil, &worker.Agent{Adapter: &planWriterAdapter{Mock: agentrunner.NewMock()}})
	w.HandleMessage(ws.WSMessage{Type: "epic_plan", JobID: &job.ID})
	waitForPlanJob(t, gdb, e.ID, epics.PlanJobDone)

	var after db.Epic
	if err := gdb.First(&after, "id = ?", e.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != epics.StateRunning {
		t.Fatalf("state = %q, want running", after.State)
	}
	var rev db.EpicPlanRevision
	if err := gdb.Where("epic_id = ?", e.ID).First(&rev).Error; err != nil {
		t.Fatal(err)
	}
	if rev.ApprovedBy != "auto:plan-generation" || after.ApprovedPlanHash != rev.PlanHash {
		t.Fatalf("auto approval not recorded/bound: %+v / %q", rev, after.ApprovedPlanHash)
	}
	var children int64
	gdb.Model(&db.Ticket{}).Where("epic_id = ?", e.ID).Count(&children)
	if children != 1 {
		t.Fatalf("children = %d, want 1", children)
	}
}

// waitForPlanJob polls the database until the epic's latest job reaches
// the wanted status; the shem runs the job on a goroutine, as in
// production.
func waitForPlanJob(t *testing.T, gdb *gorm.DB, epicID, want string) *db.EpicPlanJob {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		job, err := epics.LatestPlanJob(gdb, epicID)
		if err == nil && job != nil && job.Status == want {
			return job
		}
		time.Sleep(50 * time.Millisecond)
	}
	job, _ := epics.LatestPlanJob(gdb, epicID)
	t.Fatalf("job never reached %s: %+v", want, job)
	return nil
}
