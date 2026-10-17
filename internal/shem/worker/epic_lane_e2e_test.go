package worker

// Full-stack epic lane proof: a real orchestrator (HTTP handlers over
// in-memory SQLite), the real shem executor stages, the real plain-git
// forge, and the real golem CLI for local ticket scaffolding. The only
// fake is the model adapter — the agent's model calls — because the merge
// lane must work with no model vendor and no GitHub in the loop at all.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/forge"
	"github.com/leonp92/golem/internal/orchestrator/api"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// laneAdapter stubs the model calls. The tester stage's RunPhase emits a
// real verdict against the worktree's measured head; anything else (a PR
// description) answers with an error, which the executor swallows by
// design — that keeps this test free of vendor behavior it does not test.
type laneAdapter struct {
	*agentrunner.Mock
	verdict string // empty means PASS; tests can exercise the real fail/retest path
}

func (a laneAdapter) RunPhase(_ context.Context, worktree, _ string, out io.Writer, _ string) error {
	head, err := worktreeHead(context.Background(), worktree)
	if err != nil {
		return err
	}
	verdict := a.verdict
	if verdict == "" {
		verdict = "PASS"
	}
	_, err = fmt.Fprintf(out, "TESTER-VERDICT: %s\nHEAD: %s\nSCENARIOS: e2e lane\nEVIDENCE: e2e lane\n", verdict, head)
	return err
}

// laneStack is the orchestrator half: real handlers, real SQLite, one
// admin session and one registered shem.
type laneStack struct {
	t      *testing.T
	gdb    *gorm.DB
	srv    *httptest.Server
	cookie *http.Cookie
	shem   *client.Client
}

func newLaneStack(t *testing.T, remote string) *laneStack {
	t.Helper()
	gdb, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := gdb.DB()
	sqlDB.SetMaxOpenConns(1)

	h := api.NewHandlers(gdb, ws.NewHub(), sse.NewBroker())
	mux := http.NewServeMux()
	h.RegisterHumanRoutes(mux)
	h.RegisterTicketRoutes(mux)
	h.RegisterEpicRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	user := db.User{Username: "lane", PasswordHash: "x", Role: "admin"}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := auth.CreateSession(gdb, rec, user.ID, false); err != nil {
		t.Fatal(err)
	}
	repos, _ := json.Marshal([]string{remote})
	hash, _ := bcrypt.GenerateFromPassword([]byte("lane-key"), bcrypt.MinCost)
	shem := db.Shem{Name: "lane-shem", APIKeyHash: string(hash), Repos: string(repos),
		Status: "online", SupportsEpics: true}
	if err := gdb.Create(&shem).Error; err != nil {
		t.Fatal(err)
	}
	return &laneStack{t: t, gdb: gdb, srv: srv, cookie: rec.Result().Cookies()[0],
		shem: client.New(srv.URL, "lane-key", "lane-shem")}
}

func (s *laneStack) human(method, path string, body any) int {
	s.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, s.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(s.cookie)
	req.Header.Set(auth.CSRFHeader, auth.CSRFTokenForSession(s.cookie.Value))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// startLaneEpic drives an epic from create to running with the given plan
// nodes and edges, returning its ID. Acceptance commands are a no-op: the
// verdict comes from the tester stage, which the laneAdapter answers.
func (s *laneStack) startLaneEpic(remote string, nodes []map[string]any, edges []map[string]any) string {
	s.t.Helper()
	if code := s.human("POST", "/api/epics", map[string]any{
		"repo_remote": remote, "title": "lane epic", "spec_markdown": "spec",
		"base_branch": "main", "auto_merge_children": true, "auto_approve_phases": true,
	}); code != http.StatusCreated {
		s.t.Fatalf("create epic: %d", code)
	}
	var e db.Epic
	s.gdb.Order("created_at desc").First(&e)

	plan := map[string]any{
		"requirements": []map[string]any{{"id": "r1", "text": "the lane works"}},
		"nodes":        nodes,
		"edges":        edges,
	}
	if code := s.human("POST", "/api/epics/"+e.ID+"/revisions", plan); code != http.StatusCreated {
		s.t.Fatalf("add revision: %d", code)
	}
	var rev db.EpicPlanRevision
	s.gdb.Where("epic_id = ?", e.ID).Order("revision desc").First(&rev)
	if code := s.human("POST", "/api/epics/"+e.ID+"/approve",
		map[string]any{"revision": rev.Revision, "plan_hash": rev.PlanHash}); code != http.StatusOK && code != http.StatusNoContent {
		s.t.Fatalf("approve: %d", code)
	}
	if code := s.human("POST", "/api/epics/"+e.ID+"/start", map[string]any{}); code != http.StatusOK && code != http.StatusNoContent {
		s.t.Fatalf("start: %d", code)
	}
	return e.ID
}

func laneNode(key, title string) map[string]any {
	return map[string]any{"key": key, "title": title, "scope": title,
		"acceptance_commands": []string{"true"}, "requirement_ids": []string{"r1"}}
}

// golemBin builds the real CLI once for the whole package run.
var golemBin struct {
	path string
	err  error
	once sync.Once
}

func buildLaneCLI(t *testing.T) string {
	t.Helper()
	golemBin.once.Do(func() {
		dir, err := os.MkdirTemp("", "golem-lane-bin-")
		if err != nil {
			golemBin.err = err
			return
		}
		golemBin.path = filepath.Join(dir, "golem")
		cmd := exec.Command("go", "build", "-o", golemBin.path, "github.com/leonp92/golem/cmd/golem")
		if out, err := cmd.CombinedOutput(); err != nil {
			golemBin.err = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if golemBin.err != nil {
		t.Fatal(golemBin.err)
	}
	return golemBin.path
}

func runCLI(t *testing.T, bin string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("golem %v: %v\n%s", args, err, out)
	}
}

func laneGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// laneRepos builds the two deployment shapes. Push-enabled: a bare remote
// and a clone the shem works in. Local-only: one working repository with
// no remote at all. Both are seeded with a main branch. Paths stay
// lowercase because the orchestrator normalizes remotes by lowercasing.
func laneRepos(t *testing.T, pushEnabled bool) (remote, repoPath string) {
	t.Helper()
	root, err := os.MkdirTemp("", "golem-lane-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	laneGit(t, seed, "init", "-q", "-b", "main")
	laneGit(t, seed, "config", "user.email", "lane@example.com")
	laneGit(t, seed, "config", "user.name", "lane")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("lane fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, seed, "add", ".")
	laneGit(t, seed, "commit", "-qm", "initial")

	if !pushEnabled {
		return "local://test", seed
	}
	remote = filepath.Join(root, "remote")
	repoPath = filepath.Join(root, "repo")
	laneGit(t, "", "clone", "-q", "--bare", seed, remote)
	laneGit(t, "", "clone", "-q", remote, repoPath)
	laneGit(t, repoPath, "config", "user.email", "lane@example.com")
	laneGit(t, repoPath, "config", "user.name", "lane")
	return remote, repoPath
}

func laneConfig(remote, repoPath string, noPush bool) *config.Config {
	return &config.Config{
		NoPush: noPush,
		Repos: []config.RepoConfig{{Path: repoPath, Remote: remote,
			NormalizedRemote: urlnorm.Normalize(remote)}},
	}
}

// laneChildClaims claims one ready child of the run and scaffolds its
// local ticket with the real CLI, the way the executor's runGolemTicketNew
// does, then simulates the agent: one committed file and the review gate
// passed. Returns the claim and the ticket dir.
func (s *laneStack) laneChildClaims(t *testing.T, bin, epicID, key, repoPath, baseRef string) (*client.ClaimResponse, string) {
	t.Helper()
	ticketID := fmt.Sprintf("epic-%s-%s", epicID[:8], strings.ToLower(key))
	claim, err := s.shem.ClaimTicket(ticketID)
	if err != nil {
		t.Fatalf("claim %s: %v", ticketID, err)
	}
	baseSHA := laneGit(t, repoPath, "rev-parse", baseRef)
	runCLI(t, bin, "ticket", "new", "--repo", repoPath, "--ticket-id", ticketID,
		"--branch", claim.Branch, "--base", baseSHA, "--", "lane child "+key)
	ticketDir := filepath.Join(repoPath, ".golem", "tickets", ticketID)
	worktree := filepath.Join(ticketDir, "worktree")
	if err := os.WriteFile(filepath.Join(worktree, key+".go"),
		[]byte("package lane\n\n// "+key+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, worktree, "add", ".")
	laneGit(t, worktree, "commit", "-qm", "work "+key)
	runCLI(t, bin, "ticket", "advance", "--repo", repoPath, "--ticket", ticketID, "--to", "review")
	return claim, ticketDir
}

func laneExecutor() *GolemExecutor {
	return &GolemExecutor{
		Agent: &Agent{Adapter: laneAdapter{Mock: agentrunner.NewMock()}},
		Forge: forge.Git{},
	}
}

// laneTargetSHA reads the run's target ref where the deployment shape put
// it: the bare remote when pushes are on, the local repo when they are off.
func laneTargetSHA(t *testing.T, remote, repoPath, targetRef string, pushEnabled bool) string {
	t.Helper()
	if pushEnabled {
		out := laneGit(t, "", "ls-remote", remote, "refs/heads/"+targetRef)
		fields := strings.Fields(out)
		if len(fields) == 0 {
			t.Fatalf("target %s missing on the remote", targetRef)
		}
		return fields[0]
	}
	return laneGit(t, repoPath, "rev-parse", "refs/heads/"+targetRef)
}

func laneEpicState(t *testing.T, gdb *gorm.DB, epicID string) string {
	t.Helper()
	var e db.Epic
	if err := gdb.First(&e, "id = ?", epicID).Error; err != nil {
		t.Fatal(err)
	}
	return e.State
}

func laneTicketPhase(t *testing.T, gdb *gorm.DB, ticketID string) string {
	t.Helper()
	var tk db.Ticket
	if err := gdb.First(&tk, "id = ?", ticketID).Error; err != nil {
		t.Fatal(err)
	}
	return tk.Phase
}

// The core proof: a three-node dependent epic runs to awaiting-final-review
// with lane merging through plain git, plus a standalone ticket alongside —
// in both deployment shapes. No GitHub token, client, or add-on exists in
// this stack.
func testEpicLaneMatrix(t *testing.T, pushEnabled bool) {
	remote, repoPath := laneRepos(t, pushEnabled)
	cfg := laneConfig(remote, repoPath, !pushEnabled)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ctx := context.Background()
	ex := laneExecutor()

	epicID := stack.startLaneEpic(remote,
		[]map[string]any{laneNode("A", "first"), laneNode("B", "second"), laneNode("C", "third")},
		[]map[string]any{
			{"predecessor": "A", "successor": "B", "reason": "B builds on A"},
			{"predecessor": "B", "successor": "C", "reason": "C builds on B"},
		})
	targetRef := "epic/" + epicID

	// B must not be claimable before A is integrated.
	bID := fmt.Sprintf("epic-%s-%s", epicID[:8], "b")
	if _, err := stack.shem.ClaimTicket(bID); err == nil {
		t.Fatalf("claimed blocked child %s before its predecessor integrated", bID)
	}

	// A merges first; its target ref does not exist yet anywhere.
	claimA, dirA := stack.laneChildClaims(t, bin, epicID, "A", repoPath, "main")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimA, repoPath, dirA, "Implementation", claimA.Branch); err != nil {
		t.Fatalf("finish A: %v", err)
	}
	laneTargetSHA(t, remote, repoPath, targetRef, pushEnabled)

	// B and C build on the moving target. A pushing shem fetches the
	// target before pinning the next child's base — exactly what the
	// executor's resolveBaseSHA does on a real run.
	for _, key := range []string{"B", "C"} {
		base := laneTargetSHA(t, remote, repoPath, targetRef, pushEnabled)
		if pushEnabled {
			laneGit(t, repoPath, "fetch", "-q", remote, "refs/heads/"+targetRef+":refs/golem-e2e/base")
			base = laneGit(t, repoPath, "rev-parse", "refs/golem-e2e/base")
		}
		claim, dir := stack.laneChildClaims(t, bin, epicID, key, repoPath, base)
		if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claim, repoPath, dir, "Implementation", claim.Branch); err != nil {
			t.Fatalf("finish %s: %v", key, err)
		}
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}

	// The target ref carries every child's commit, in order.
	show := func(path string) string {
		if pushEnabled {
			return laneGit(t, "", "--git-dir", remote, "show", targetRef+":"+path)
		}
		return laneGit(t, repoPath, "show", targetRef+":"+path)
	}
	for _, key := range []string{"A", "B", "C"} {
		if body := show(key + ".go"); !strings.Contains(body, "// "+key) {
			t.Fatalf("target is missing %s's commit: %q", key, body)
		}
	}

	// The final merge is a human act: the attestation records the SHA of
	// the integration branch merged to base, and the run completes.
	if pushEnabled {
		final := filepath.Join(t.TempDir(), "final")
		laneGit(t, "", "clone", "-q", remote, final)
		laneGit(t, final, "config", "user.email", "lane@example.com")
		laneGit(t, final, "config", "user.name", "lane")
		laneGit(t, final, "merge", "-q", "--no-ff", "-m", "final", "origin/"+targetRef)
		laneGit(t, final, "push", "-q", "origin", "main")
		sha := laneGit(t, final, "rev-parse", "HEAD")
		if code := stack.human("POST", "/api/epics/"+epicID+"/merge-final",
			map[string]any{"sha": sha}); code != http.StatusOK && code != http.StatusNoContent {
			t.Fatalf("merge-final: %d", code)
		}
		if got := laneEpicState(t, stack.gdb, epicID); got != "completed" {
			t.Fatalf("run state after final merge = %q, want completed", got)
		}
	}

	// A standalone ticket works the same way, merges nowhere, and opens no
	// pull request without the GitHub add-on.
	if code := stack.human("POST", "/api/tickets", map[string]any{
		"repo_remote": remote, "branch": "main", "title": "solo", "description": "standalone",
	}); code != http.StatusCreated {
		t.Fatalf("create standalone: %d", code)
	}
	var solo db.Ticket
	stack.gdb.Where("epic_id IS NULL").Order("created_at desc").First(&solo)
	claimSolo, err := stack.shem.ClaimTicket(solo.ID)
	if err != nil {
		t.Fatalf("claim standalone: %v", err)
	}
	runCLI(t, bin, "ticket", "new", "--repo", repoPath, "--ticket-id", solo.ID,
		"--branch", claimSolo.Branch, "--base", laneGit(t, repoPath, "rev-parse", "main"), "--", "standalone")
	soloDir := filepath.Join(repoPath, ".golem", "tickets", solo.ID)
	soloWt := filepath.Join(soloDir, "worktree")
	if err := os.WriteFile(filepath.Join(soloWt, "solo.go"), []byte("package lane\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, soloWt, "add", ".")
	laneGit(t, soloWt, "commit", "-qm", "solo work")
	runCLI(t, bin, "ticket", "advance", "--repo", repoPath, "--ticket", solo.ID, "--to", "review")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimSolo, repoPath, soloDir, "Implementation", claimSolo.Branch); err != nil {
		t.Fatalf("finish standalone: %v", err)
	}
	if got := laneTicketPhase(t, stack.gdb, solo.ID); got != "ready-for-review" {
		t.Fatalf("standalone phase = %q, want ready-for-review", got)
	}
	var prs int64
	stack.gdb.Model(&db.Ticket{}).Where("id = ? AND pr_number IS NOT NULL AND pr_number != 0", solo.ID).Count(&prs)
	if prs != 0 {
		t.Fatalf("standalone ticket got a pull request with no GitHub add-on")
	}
}

func TestEpicLaneE2E_PushEnabled_BareRemote(t *testing.T) {
	testEpicLaneMatrix(t, true)
}

func TestEpicLaneE2E_PushDisabled_LocalOnly(t *testing.T) {
	testEpicLaneMatrix(t, false)
}

// A conflicting child is bounced back to revising with the conflict as
// feedback, and its fixed head merges on the retry.
func TestEpicLaneE2E_ConflictBounce(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ctx := context.Background()
	ex := laneExecutor()

	epicID := stack.startLaneEpic(remote,
		[]map[string]any{laneNode("X", "one"), laneNode("Y", "two")}, nil)
	targetRef := "epic/" + epicID

	// Both children touch the same file differently, from the same base.
	claimX, dirX := stack.laneChildClaims(t, bin, epicID, "X", repoPath, "main")
	wtX := filepath.Join(dirX, "worktree")
	if err := os.WriteFile(filepath.Join(wtX, "shared.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtX, "add", ".")
	laneGit(t, wtX, "commit", "-qm", "x writes shared")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimX, repoPath, dirX, "Implementation", claimX.Branch); err != nil {
		t.Fatalf("finish X: %v", err)
	}

	claimY, dirY := stack.laneChildClaims(t, bin, epicID, "Y", repoPath, "main")
	wtY := filepath.Join(dirY, "worktree")
	if err := os.WriteFile(filepath.Join(wtY, "shared.txt"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "y writes shared")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimY, repoPath, dirY, "Implementation", claimY.Branch); err != nil {
		t.Fatalf("finish Y: %v", err)
	}
	yID := fmt.Sprintf("epic-%s-%s", epicID[:8], "y")
	if got := laneTicketPhase(t, stack.gdb, yID); got != "revising" {
		t.Fatalf("conflicted child phase = %q, want revising", got)
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "running" {
		t.Fatalf("run state during bounce = %q, want running", got)
	}

	// The revision merges the target into the branch and resolves.
	laneGit(t, wtY, "fetch", remote, "refs/heads/"+targetRef)
	mergeCmd := exec.Command("git", "merge", "--no-edit", "FETCH_HEAD") // conflicts, resolved below
	mergeCmd.Dir = wtY
	_ = mergeCmd.Run()
	if err := os.WriteFile(filepath.Join(wtY, "shared.txt"), []byte("x\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wtY, "add", ".")
	laneGit(t, wtY, "commit", "-qm", "resolve with the target")
	if err := ex.finishWorkPhase(ctx, cfg, stack.shem, claimY, repoPath, dirY, "Implementation", claimY.Branch); err != nil {
		t.Fatalf("refinish Y: %v", err)
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state after resolution = %q, want awaiting-final-review", got)
	}
	if body := laneGit(t, "", "--git-dir", remote, "show", targetRef+":shared.txt"); body != "x\ny" {
		t.Fatalf("resolved content = %q", body)
	}
}

// A shem that dies between the tester verdict and the merge owes the lane;
// the resume entry (startup sweep, epic_resumed push) runs it again.
func TestEpicLaneE2E_RestartResume(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ctx := context.Background()
	ex := laneExecutor()

	epicID := stack.startLaneEpic(remote, []map[string]any{laneNode("A", "first")}, nil)
	targetRef := "epic/" + epicID
	aID := fmt.Sprintf("epic-%s-%s", epicID[:8], "a")

	claim, dir := stack.laneChildClaims(t, bin, epicID, "A", repoPath, "main")
	worktree := filepath.Join(dir, "worktree")

	// Everything up to the lane, then the "crash": push, phase, verdict.
	if err := pushTicketBranch(ctx, repoPath, worktree, claim.Branch, claim.RepoRemote); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := stack.shem.PostBranchPushed(claim.TicketID, ""); err != nil {
		t.Fatalf("branch-pushed: %v", err)
	}
	if err := stack.shem.PostPhase(claim.TicketID, "ready-for-review"); err != nil {
		t.Fatalf("post phase: %v", err)
	}
	if v := ex.runTesterStage(ctx, stack.shem, claim, repoPath, worktree, dir); v != "pass" {
		t.Fatalf("tester verdict = %q, want pass", v)
	}

	if got := laneTicketPhase(t, stack.gdb, aID); got != "ready-for-review" {
		t.Fatalf("pre-resume phase = %q", got)
	}
	// Crash after the remote merge but before posting its receipt. Recovery
	// must discover the contained head and post the missing receipt.
	head := laneGit(t, worktree, "rev-parse", "HEAD")
	gate, err := stack.shem.CheckIntegrationGate(epicID, "A", head)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := ex.Forge.MergeIntoTarget(ctx, forge.Merge{
		Mirror: pushMirrorPath(repoPath), Remote: remote, Branch: claim.Branch,
		HeadSHA: head, TargetRef: gate.TargetRef,
	})
	if err != nil || merged.Status != forge.Merged {
		t.Fatalf("merge before crash: %v %v", merged, err)
	}
	// The resume entry point runs the lane to completion.
	ex.IntegrateAssigned(ctx, cfg, stack.shem, claim, repoPath, claim.Branch)

	var child db.Ticket
	if err := stack.gdb.First(&child, "id = ?", aID).Error; err != nil {
		t.Fatal(err)
	}
	if child.EpicIntegratedSHA == "" {
		t.Fatalf("resume recorded no receipt")
	}
	laneTargetSHA(t, remote, repoPath, targetRef, true)
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}

	// The receipt is idempotent: a second resume is a no-op, not an error.
	ex.IntegrateAssigned(ctx, cfg, stack.shem, claim, repoPath, claim.Branch)
	var again db.Ticket
	if err := stack.gdb.First(&again, "id = ?", aID).Error; err != nil {
		t.Fatal(err)
	}
	if again.EpicIntegratedSHA != child.EpicIntegratedSHA {
		t.Fatalf("second resume changed the receipt: %q -> %q", child.EpicIntegratedSHA, again.EpicIntegratedSHA)
	}
}

// The deployment the question came from: a real remote is configured but
// pushes are off. Children merge into the epic branch locally and the
// remote stays exactly as it was — no ticket branches, no target ref.
func TestEpicLaneE2E_PushDisabled_RemoteConfigured(t *testing.T) {
	remote, repoPath := laneRepos(t, true)
	cfg := laneConfig(remote, repoPath, true)
	stack := newLaneStack(t, remote)
	if err := stack.gdb.Create(&db.GitHubRepo{RepoRemote: remote, Owner: "org", Name: "repo", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repoPath, "--backend", "claude-code")
	ex := laneExecutor()

	epicID := stack.startLaneEpic(remote,
		[]map[string]any{laneNode("A", "first"), laneNode("B", "second")},
		[]map[string]any{{"predecessor": "A", "successor": "B", "reason": "B builds on A"}})
	targetRef := "epic/" + epicID

	claimA, dirA := stack.laneChildClaims(t, bin, epicID, "A", repoPath, "main")
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, claimA, repoPath, dirA, "Implementation", claimA.Branch); err != nil {
		t.Fatalf("finish A: %v", err)
	}
	claimB, dirB := stack.laneChildClaims(t, bin, epicID, "B", repoPath,
		laneGit(t, repoPath, "rev-parse", "refs/heads/"+targetRef))
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, claimB, repoPath, dirB, "Implementation", claimB.Branch); err != nil {
		t.Fatalf("finish B: %v", err)
	}
	if got := laneEpicState(t, stack.gdb, epicID); got != "awaiting-final-review" {
		t.Fatalf("run state = %q, want awaiting-final-review", got)
	}
	if body := laneGit(t, repoPath, "show", targetRef+":B.go"); !strings.Contains(body, "// B") {
		t.Fatalf("local target is missing B's commit: %q", body)
	}
	// The remote never saw any of it.
	out := laneGit(t, "", "ls-remote", remote)
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasSuffix(line, "refs/heads/main") && !strings.HasSuffix(line, "\tHEAD") {
			t.Fatalf("remote carries %q with pushes disabled", line)
		}
	}
	var queued int64
	stack.gdb.Model(&db.GitHubOutbox{}).Where("epic_id = ?", epicID).Count(&queued)
	if queued != 0 {
		t.Fatal("no_push integration queued a remote final PR")
	}

}

// Repeated unfixable conflicts stop at the configured attempt cap rather
// than dispatching an agent forever. This exercises the live result route.
func TestEpicLaneE2E_ConflictAttemptLimit(t *testing.T) {
	t.Setenv("GOLEM_FIX_ATTEMPTS", "1")
	remote := "https://git.example.com/org/repo"
	stack := newLaneStack(t, remote)
	var shem db.Shem
	if err := stack.gdb.First(&shem, "name = ?", "lane-shem").Error; err != nil {
		t.Fatal(err)
	}
	epicID := stack.startLaneEpic(remote, []map[string]any{laneNode("A", "conflict")}, nil)
	var child db.Ticket
	if err := stack.gdb.First(&child, "epic_id = ?", epicID).Error; err != nil {
		t.Fatal(err)
	}
	if err := stack.gdb.Model(&child).Updates(map[string]any{"phase": "ready-for-review", "assigned_shem": shem.ID, "pr_fix_attempts": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := stack.gdb.Create(&db.TesterVerdict{EpicID: epicID, EpicNodeKey: "A", HeadSHA: "head", Verdict: "pass"}).Error; err != nil {
		t.Fatal(err)
	}
	gate, err := stack.shem.CheckIntegrationGate(epicID, "A", "head")
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.shem.PostIntegrationResult(epicID, "A", client.IntegrationResult{
		LeaseID: gate.LeaseID, HeadSHA: "head", Status: "conflict", Detail: "shared.txt",
	}); err != nil {
		t.Fatal(err)
	}
	if got := laneTicketPhase(t, stack.gdb, child.ID); got != "needs-attention" {
		t.Fatalf("conflict beyond cap: phase=%s", got)
	}
}

// Model-independent recovery crosses the real HTTP, tester and Git boundaries.
// Every operational failure is retried by the same assigned reconciliation
// used by the periodic poll, without restarting or manually resuming the run.
type recoveryForge struct {
	forge.Forge
	fail string
}

func (f *recoveryForge) HeadSHA(ctx context.Context, remote, branch string) (string, error) {
	if f.fail == "head" {
		f.fail = ""
		return "", errors.New("temporary remote outage")
	}
	return f.Forge.HeadSHA(ctx, remote, branch)
}
func (f *recoveryForge) MergeIntoTarget(ctx context.Context, op forge.Merge) (forge.Result, error) {
	if f.fail == "merge" {
		f.fail = ""
		return forge.Result{}, errors.New("temporary fetch outage")
	}
	return f.Forge.MergeIntoTarget(ctx, op)
}
func TestEpicLaneE2E_OperationalRecovery(t *testing.T) {
	for _, failure := range []string{"head", "gate", "merge", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			remote, repo := laneRepos(t, true)
			cfg := laneConfig(remote, repo, false)
			stack := newLaneStack(t, remote)
			bin := buildLaneCLI(t)
			runCLI(t, bin, "init", "--repo", repo, "--backend", "claude-code")
			id := stack.startLaneEpic(remote, []map[string]any{laneNode("A", "one")}, nil)
			claim, dir := stack.laneChildClaims(t, bin, id, "A", repo, "main")
			ex := laneExecutor()
			ex.Forge = &recoveryForge{Forge: forge.Git{}, fail: failure}
			var failRequest sync.Once
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				matches := failure == "gate" && strings.HasSuffix(r.URL.Path, "/integrate-check") || failure == "receipt" && strings.HasSuffix(r.URL.Path, "/integrate-result")
				blocked := false
				if matches {
					failRequest.Do(func() { blocked = true })
				}
				if blocked {
					http.Error(w, "temporary unavailable response", http.StatusBadRequest)
					return
				}
				stack.srv.Config.Handler.ServeHTTP(w, r)
			}))
			defer proxy.Close()
			c := client.New(proxy.URL, "lane-key", "lane-shem")
			if err := ex.finishWorkPhase(context.Background(), cfg, c, claim, repo, dir, "Implementation", claim.Branch); err != nil {
				t.Fatal(err)
			}
			var child db.Ticket
			stack.gdb.First(&child, "id = ?", claim.TicketID)
			if child.EpicIntegratedSHA != "" {
				t.Fatal("fault did not interrupt receipt")
			}
			assigned, err := c.GetAssigned()
			if err != nil {
				t.Fatal(err)
			}
			w := New(cfg, c, ex, nil)
			w.integrateAssigned(assigned)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				stack.gdb.First(&child, "id = ?", claim.TicketID)
				w.mu.Lock()
				busy := len(w.running) != 0
				w.mu.Unlock()
				if child.EpicIntegratedSHA != "" && !busy {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			w.mu.Lock()
			busy := len(w.running) != 0
			w.mu.Unlock()
			if child.EpicIntegratedSHA == "" || busy {
				t.Fatal("periodic reconciliation did not recover integration")
			}
			if got := laneEpicState(t, stack.gdb, id); got != "awaiting-final-review" {
				t.Fatalf("state=%s", got)
			}
		})
	}
}

// The tester failure path must work independently of merge conflicts: fail
// the measured head, revise the same branch, retest the new head, then merge.
func TestEpicLaneE2E_TesterFailFixRetest(t *testing.T) {
	remote, repo := laneRepos(t, true)
	cfg := laneConfig(remote, repo, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repo, "--backend", "claude-code")
	id := stack.startLaneEpic(remote, []map[string]any{laneNode("A", "one")}, nil)
	claim, dir := stack.laneChildClaims(t, bin, id, "A", repo, "main")
	wt := filepath.Join(dir, "worktree")
	oldHead := laneGit(t, wt, "rev-parse", "HEAD")
	ex := laneExecutor()
	ex.Agent.Adapter = laneAdapter{Mock: &agentrunner.Mock{}, verdict: "FAIL"}
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, claim, repo, dir, "Implementation", claim.Branch); err != nil {
		t.Fatal(err)
	}
	var child db.Ticket
	stack.gdb.First(&child, "id = ?", claim.TicketID)
	if child.Phase != "revising" || child.PRFixAttempts != 1 || child.EpicIntegratedSHA != "" {
		t.Fatalf("tester fail did not request a fix: %+v", child)
	}
	if _, err := stack.shem.CheckIntegrationGate(id, "A", oldHead); err == nil {
		t.Fatal("failed head got an integration lease")
	}
	revision, err := stack.shem.ClaimRevision(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "fix.txt"), []byte("fixed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	laneGit(t, wt, "add", "fix.txt")
	laneGit(t, wt, "commit", "-qm", "fix tester failure")
	runCLI(t, bin, "ticket", "advance", "--repo", repo, "--ticket", child.ID, "--to", "review")
	ex.Agent.Adapter = laneAdapter{Mock: &agentrunner.Mock{}}
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, revision, repo, dir, "Revision", revision.Branch); err != nil {
		t.Fatal(err)
	}
	stack.gdb.First(&child, "id = ?", claim.TicketID)
	if child.Phase != "ready-for-review" || child.EpicIntegratedSHA == "" {
		t.Fatalf("fixed/retested child not integrated: %+v", child)
	}
	newHead := laneGit(t, wt, "rev-parse", "HEAD")
	var verdicts []db.TesterVerdict
	stack.gdb.Where("epic_id = ?", id).Order("id").Find(&verdicts)
	if len(verdicts) != 2 || verdicts[0].HeadSHA != oldHead || verdicts[0].Verdict != "fail" || verdicts[1].HeadSHA != newHead || verdicts[1].Verdict != "pass" || newHead == oldHead {
		t.Fatalf("incorrect verdict history: %+v", verdicts)
	}
	if got := laneEpicState(t, stack.gdb, id); got != "awaiting-final-review" {
		t.Fatalf("state=%s", got)
	}
	if err := stack.shem.PostEpicVerdict(id, client.EpicVerdictPayload{EpicNodeKey: "A", HeadSHA: oldHead, Verdict: "inconclusive", Evidence: "delayed old result"}); err != nil {
		t.Fatal(err)
	}
	if got := laneTicketPhase(t, stack.gdb, child.ID); got != "ready-for-review" {
		t.Fatalf("late verdict changed integrated child: %s", got)
	}
}

// Pausing an active run must let claimed work finish without integrating
// the child. Resuming makes it eligible for the existing reconciliation.
func TestEpicLaneE2E_FinishWhilePausedThenResume(t *testing.T) {
	remote, repo := laneRepos(t, true)
	cfg := laneConfig(remote, repo, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repo, "--backend", "claude-code")
	id := stack.startLaneEpic(remote, []map[string]any{laneNode("A", "one")}, nil)
	claim, dir := stack.laneChildClaims(t, bin, id, "A", repo, "main")
	if code := stack.human("POST", "/api/epics/"+id+"/pause", nil); code != http.StatusNoContent {
		t.Fatalf("pause=%d", code)
	}
	ex := laneExecutor()
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, claim, repo, dir, "Implementation", claim.Branch); err != nil {
		t.Fatal(err)
	}
	var child db.Ticket
	if err := stack.gdb.First(&child, "id = ?", claim.TicketID).Error; err != nil {
		t.Fatal(err)
	}
	if child.Phase != "ready-for-review" || child.EpicIntegratedSHA != "" {
		t.Fatalf("paused child incorrectly integrated: %+v", child)
	}
	if got := laneEpicState(t, stack.gdb, id); got != "paused" {
		t.Fatalf("state=%s", got)
	}
	var slots int64
	stack.gdb.Model(&db.EpicIntegrationSlot{}).Where("epic_id = ?", id).Count(&slots)
	if slots != 0 {
		t.Fatal("paused child acquired a merge lease")
	}
	if code := stack.human("POST", "/api/epics/"+id+"/resume", nil); code != http.StatusNoContent {
		t.Fatalf("resume=%d", code)
	}
	ex.IntegrateAssigned(context.Background(), cfg, stack.shem, claim, repo, claim.Branch)
	if err := stack.gdb.First(&child, "id = ?", claim.TicketID).Error; err != nil {
		t.Fatal(err)
	}
	if child.EpicIntegratedSHA == "" || laneEpicState(t, stack.gdb, id) != "awaiting-final-review" {
		t.Fatal("resumed child did not integrate")
	}
}

// no_push is a worker-wide setting. A local:// repository remains local
// when the same worker enables publishing for its other repositories.
func TestEpicLaneE2E_LocalRepositoryWithPublishingEnabled(t *testing.T) {
	remote, repo := laneRepos(t, false)
	cfg := laneConfig(remote, repo, false)
	stack := newLaneStack(t, remote)
	bin := buildLaneCLI(t)
	runCLI(t, bin, "init", "--repo", repo, "--backend", "claude-code")
	id := stack.startLaneEpic(remote, []map[string]any{laneNode("A", "one")}, nil)
	claim, dir := stack.laneChildClaims(t, bin, id, "A", repo, "main")
	ex := laneExecutor()
	if err := ex.finishWorkPhase(context.Background(), cfg, stack.shem, claim, repo, dir, "Implementation", claim.Branch); err != nil {
		t.Fatal(err)
	}
	if got := laneEpicState(t, stack.gdb, id); got != "awaiting-final-review" {
		t.Fatalf("state=%s", got)
	}
	if got := laneGit(t, repo, "show", "epic/"+id+":A.go"); !strings.Contains(got, "// A") {
		t.Fatal("local target missing child")
	}
}
