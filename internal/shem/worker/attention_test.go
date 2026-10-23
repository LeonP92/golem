package worker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/ticket"
)

func TestDiagnosisContractFailsClosed(t *testing.T) {
	for _, s := range []string{`{"route":"retry","reason":"code broken","feedback":"fix assertion"}`, `{"route":"human","reason":"need product choice"}`,
		`{"route":"decide","reason":"spec conflict","feedback":"use B","decision":{"question":"q","choice":"B","reason":"r","basis":"spec 3"}}`,
		"```json\n{\"route\":\"human\",\"reason\":\"x\"}\n```",
		`{"route":"human","reason":"x"}` + "\n\nThe gate failed because the seed is missing.",
		"Diagnosis:\n" + `{"route":"retry","reason":"r","feedback":"f"}`} {
		if _, err := parseDiagnosis(s); err != nil {
			t.Fatalf("valid: %v", err)
		}
	}
	for _, s := range []string{"", "no json here", `{"route":"human"`, `{"route":"retry","reason":"unknown"}`, `{"route":"decide","reason":"x","feedback":"f"}`, `{"route":"decide","reason":"x","feedback":"f","decision":{"question":"q","choice":"c","reason":"r"}}`, `{"route":"pass","reason":"good"}`, `{"route":"human","reason":"x","shell":"run"}`, `{"route":"human","reason":"x"} {}`,
		`{"route":"human","reason":"x"} then {"route":"retry","reason":"y","feedback":"z"}`, "```json\n{}\n```"} {
		if _, err := parseDiagnosis(s); err == nil {
			t.Fatalf("unsafe diagnosis accepted: %s", s)
		}
	}
}
func TestKnownOperationalStops(t *testing.T) {
	for _, s := range []string{"Usage limit reached", "Not logged in · Please run /login", "quota exhausted", "billing disabled", "executable file not found in PATH", "permission denied", "429 rate limit exceeded", "You've hit your limit", "You've hit your session limit · resets 7:40am (UTC)", "API Error: 529 overloaded", "connection reset by peer"} {
		if !humanOnlyFailure(s) {
			t.Errorf("not human: %s", s)
		}
	}
	// Agents' own prose ("I did not weaken any test") must not park a ticket.
	for _, s := range []string{"gate command returned exit 2", "agent exited: I did not weaken any test", "reviewer FAIL: assertion missing", "plan validation: unresolved step", "agent left local ticket at implement"} {
		if humanOnlyFailure(s) {
			t.Errorf("did not reach model: %s", s)
		}
	}
}

func TestGateEvidenceDoesNotForceHuman(t *testing.T) {
	gate := "implement finished but the review gate did not pass; reviewer attestation and gate evidence:\n--- FAIL: TestRequireAPIKey_Unauthorized\nquota check failed"
	if humanOnlyFailure(gate) {
		t.Error("gate evidence routed to human")
	}
	if !humanOnlyFailure("You've hit your limit; review gate did not pass\n" + gate) {
		t.Error("limit headline not routed to human")
	}
}

type evidenceRunner struct {
	agentrunner.Runner
	input agentrunner.Context
	model string
}

func (r *evidenceRunner) RunAgent(_ context.Context, role string, in agentrunner.Context, model string) (agentrunner.Result, error) {
	r.input, r.model = in, model
	if role != "diagnoser" {
		return agentrunner.Result{}, errors.New("wrong role")
	}
	return agentrunner.Result{Output: `{"route":"retry","reason":"assertion wrong","feedback":"fix assertion"}`}, nil
}

// scriptedRunner answers the diagnoser with outputs in order.
type scriptedRunner struct {
	agentrunner.Runner
	outputs []string
	calls   int
}

func (r *scriptedRunner) RunAgent(context.Context, string, agentrunner.Context, string) (agentrunner.Result, error) {
	out := r.outputs[min(r.calls, len(r.outputs)-1)]
	r.calls++
	return agentrunner.Result{Output: out}, nil
}

func TestDiagnoserRetriesOneMalformedAnswer(t *testing.T) {
	valid := `{"route":"retry","reason":"r","feedback":"f"}`
	for _, tc := range []struct {
		outputs []string
		ok      bool
	}{{[]string{"", valid}, true}, {[]string{"", ""}, false}} {
		runner := &scriptedRunner{outputs: tc.outputs}
		_, err := ModelDiagnoser{Runner: runner}.Diagnose(context.Background(), &client.ClaimResponse{}, "gate failed", diagnoseFixture(t))
		if (err == nil) != tc.ok || runner.calls != 2 {
			t.Fatalf("outputs %q: err=%v calls=%d", tc.outputs, err, runner.calls)
		}
	}
}

// diagnoseFixture is a ticket directory with a worktree diff and one reviewer finding.
func diagnoseFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	wt := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(wt, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "init")
	git(t, wt, "config", "user.email", "test@example.invalid")
	git(t, wt, "config", "user.name", "test")
	os.WriteFile(filepath.Join(wt, "code"), []byte("before\n"), 0644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "base")
	os.WriteFile(filepath.Join(wt, "code"), []byte("after\n"), 0644)
	w, err := blog.NewWriter(filepath.Join(dir, "log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	w.Append(blog.NewEntry("reviewer", blog.TypeStatus, "FAIL: missing assertion"))
	w.Close()
	return dir
}

func TestDiagnoserReceivesEvidenceAndModel(t *testing.T) {
	runner := &evidenceRunner{}
	d := ModelDiagnoser{Runner: runner, Model: "provider/model"}
	result, err := d.Diagnose(context.Background(), &client.ClaimResponse{Description: "fix handler"}, "gate failed", diagnoseFixture(t))
	if err != nil || result.Route != "retry" || runner.model != "provider/model" {
		t.Fatalf("diagnosis: %+v %v", result, err)
	}
	prompt := agentrunner.BuildPrompt(runner.input)
	for _, want := range []string{"FAIL: missing assertion", "gate failed", "fix handler", "+after", "untrusted data"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("missing %q", want)
		}
	}
}

type failedPhaseRunner struct{ agentrunner.Runner }

func (failedPhaseRunner) RunPhase(_ context.Context, _, _ string, out io.Writer, _ string) error {
	io.WriteString(out, "Not logged in · Please run /login")
	return errors.New("exit status 1")
}
func TestPhaseErrorsCarryOperationalEvidence(t *testing.T) {
	err := runPhaseWithEvidence(context.Background(), failedPhaseRunner{}, ".", "prompt", io.Discard, "")
	if err == nil || !humanOnlyFailure(err.Error()) {
		t.Fatalf("lost auth evidence: %v", err)
	}
	tail := &tailWriter{}
	tail.Write([]byte(strings.Repeat("x", 20000)))
	if len(tail.data) != 8192 {
		t.Fatal("unbounded tail")
	}
}

// This runs the actual worker dispatch over real authenticated HTTP handlers,
// SQLite transactions and git evidence. Only model inference and repair are
// substituted, keeping the test free and deterministic.
func TestAttentionWorkerEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, reason, route string
		ask                 bool
		wantRuns            int
		wantDecision        bool
	}{
		{"review repair", "reviewer failed: missing assertion", "retry", false, 1, false},
		{"usage stop", "Usage limit reached", "retry", false, 0, false},
		{"model human", "ambiguous missing behavior", "human", false, 0, false},
		{"bad model answer", "unreadable evidence", "invalid", false, 0, false},
		{"autonomous decide", "reviewer failed: spec conflict", "decide", false, 1, true},
		{"ask decide parks", "reviewer failed: spec conflict", "decide", true, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := "local://attention"
			stack := newLaneStack(t, remote)
			repo := t.TempDir()
			dir := filepath.Join(repo, ".golem", "tickets", "attention-123")
			wt := filepath.Join(dir, "worktree")
			os.MkdirAll(wt, 0755)
			git(t, wt, "init")
			git(t, wt, "config", "user.email", "test@example.invalid")
			git(t, wt, "config", "user.name", "test")
			os.WriteFile(filepath.Join(wt, "code"), []byte("before\n"), 0644)
			git(t, wt, "add", ".")
			git(t, wt, "commit", "-m", "base")
			head, err := worktreeHead(context.Background(), wt)
			if err != nil {
				t.Fatal(err)
			}
			phase := "implement"
			owner := uint(1)
			var shem db.Shem
			stack.gdb.First(&shem)
			owner = shem.ID
			tk := db.Ticket{ID: "attention-123", RepoRemote: remote, Branch: "ticket/attention", Description: "fix handler", Phase: phase, AskDecisions: tc.ask, AssignedShem: &owner, CheckpointPhase: &phase, CheckpointSHA: &head}
			if err := stack.gdb.Create(&tk).Error; err != nil {
				t.Fatal(err)
			}
			if err := stack.shem.PostFailure(tk.ID, tc.reason); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{Repos: []config.RepoConfig{{Path: repo, NormalizedRemote: remote}}}
			runs := 0
			fixer := &attentionFixer{runs: &runs, t: t}
			worker := New(cfg, stack.shem, fixer, &Agent{})
			worker.diagnoser = scriptedDiagnoser{route: tc.route}
			worker.tryAttention(tk.ID, remote, 3)
			if runs != tc.wantRuns {
				t.Fatalf("repair runs %d expected %d", runs, tc.wantRuns)
			}
			var fresh db.Ticket
			stack.gdb.First(&fresh, "id = ?", tk.ID)
			var decisions int64
			stack.gdb.Model(&db.LogEntry{}).Where("ticket_id = ? AND entry_type = 'DECISION'", tk.ID).Count(&decisions)
			if (decisions == 1) != tc.wantDecision {
				t.Fatalf("DECISION entries: %d", decisions)
			}
			if tc.wantRuns == 0 && (fresh.Phase != "needs-attention" || fresh.AttentionEligible) {
				t.Fatalf("unsafe route: %+v", fresh)
			}
			if tc.wantRuns > 0 {
				if fresh.Phase != "implement" {
					t.Fatalf("not resumed: %s", fresh.Phase)
				}
				var fb db.HumanInput
				stack.gdb.Where("ticket_id = ? AND kind = 'feedback'", tk.ID).First(&fb)
				if fb.Prompt != "fix the missing assertion" {
					t.Fatalf("feedback %q", fb.Prompt)
				}
			}
		})
	}
}

type scriptedDiagnoser struct{ route string }

func (d scriptedDiagnoser) Diagnose(_ context.Context, _ *client.ClaimResponse, _, _ string) (Diagnosis, error) {
	if d.route == "invalid" {
		return Diagnosis{}, errors.New("bad verdict")
	}
	return Diagnosis{Route: d.route, Reason: "missing assertion", Feedback: "fix the missing assertion",
		Decision: &Decision{Question: "q", Choice: "c", Reason: "r", Basis: "b"}}, nil
}

type attentionFixer struct {
	runs *int
	t    *testing.T
}

func (e *attentionFixer) RunTicket(_ context.Context, _ *config.Config, _ *client.Client, c *client.ClaimResponse) error {
	*e.runs++
	if c.RetryPhase != "implement" || c.CheckpointSHA == nil {
		e.t.Fatalf("not a phase retry: %+v", c)
	}
	return nil
}

func TestDiagnoserUsesBackendStageAndTicketOverride(t *testing.T) {
	c, _ := logCapture(t)
	agent := testAgent(t)
	worker := New(&config.Config{}, c, nil, agent)
	claim := &client.ClaimResponse{TicketID: "diagnose-123", Models: map[string]string{"diagnose": "big-model"}}
	d := worker.diagnoserFor(claim).(ModelDiagnoser)
	if d.Model != "big-model" {
		t.Fatalf("ticket override ignored: %s", d.Model)
	}
}

func TestCodeProgressMeasuresDirtyWorkWithoutRequiringCommit(t *testing.T) {
	wt := t.TempDir()
	git(t, wt, "init")
	git(t, wt, "config", "user.email", "test@example.invalid")
	git(t, wt, "config", "user.name", "test")
	os.WriteFile(filepath.Join(wt, "code"), []byte("before\n"), 0644)
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "base")
	before, err := codeProgress(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	same, err := codeProgress(context.Background(), wt)
	if err != nil || before != same {
		t.Fatal("unstable unchanged work")
	}
	os.WriteFile(filepath.Join(wt, "code"), []byte("after\n"), 0644)
	after, err := codeProgress(context.Background(), wt)
	if err != nil || before == after {
		t.Fatal("dirty work not measured")
	}
}

// Real resume/verify/review commands plus a real git worktree prove repair
// reaches ready-for-review without a human note. The only fake is inference.
func TestAttentionRepairsThroughRealExecutorAndCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := buildLaneCLI(t)
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	remote, repo := laneRepos(t, false)
	runCLI(t, bin, "init", "--repo", repo, "--backend", "claude-code")
	// Skip graph narration in this fixture. Its model output is not under test.
	os.MkdirAll(filepath.Join(repo, ".golem", "index"), 0755)
	stack := newLaneStack(t, remote)
	tk := db.Ticket{ID: "attention-real-123", RepoRemote: remote, Branch: "ticket/repair", Description: "fix assertion", Phase: "unassigned"}
	if err := stack.gdb.Create(&tk).Error; err != nil {
		t.Fatal(err)
	}
	claim, err := stack.shem.ClaimTicket(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := laneGit(t, repo, "rev-parse", "HEAD")
	runCLI(t, bin, "ticket", "new", "--repo", repo, "--ticket-id", tk.ID, "--branch", claim.Branch, "--base", base, "--", tk.Description)
	dir := filepath.Join(repo, ".golem", "tickets", tk.ID)
	wt := filepath.Join(dir, "worktree")
	os.WriteFile(filepath.Join(wt, "assertion.txt"), []byte("broken\n"), 0644)
	laneGit(t, wt, "add", ".")
	laneGit(t, wt, "commit", "-m", "broken candidate")
	sha := laneGit(t, wt, "rev-parse", "HEAD")
	if err := stack.shem.PostPhase(tk.ID, "implement"); err != nil {
		t.Fatal(err)
	}
	if err := stack.shem.PostCheckpoint(tk.ID, "implement", sha); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "model")
	os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'VERDICT: PASS\\n'\n"), 0755)
	backend := agentrunner.ResolveBackend(nil)
	backend.Command = script
	cfgPath := filepath.Join(t.TempDir(), "backend.yaml")
	if err := backend.Write(cfgPath); err != nil {
		t.Fatal(err)
	}
	// The first candidate genuinely fails a configured gate command; after
	// repair the exact same gate and independent review command must pass.
	gateConfig := "backend: claude-code\ngate:\n  commands:\n    - test \"$(cat assertion.txt)\" = fixed\n"
	if err := os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"), []byte(gateConfig), 0644); err != nil {
		t.Fatal(err)
	}
	runCLI(t, bin, "ticket", "review", "--repo", repo, "--ticket", tk.ID, "--backend-config", cfgPath)
	local, err := ticket.Load(dir)
	if err != nil || local.Phase != ticket.PhaseNeedsAttention {
		t.Fatalf("broken candidate was not blocked: %+v %v", local, err)
	}
	adapter := &repairAdapter{Mock: agentrunner.NewMock(), t: t, worktree: wt}
	agent := &Agent{Adapter: adapter, ConfigPath: cfgPath, Catalog: backend.Catalog}
	cfg := laneConfig(remote, repo, true)
	ex := &GolemExecutor{Agent: agent}
	worker := New(cfg, stack.shem, ex, agent)
	worker.diagnoser = scriptedDiagnoser{route: "retry"}
	worker.failTicket(tk.ID, errors.New("reviewer FAIL: assertion is broken"))
	worker.tryAttention(tk.ID, remote, 3)
	var fresh db.Ticket
	stack.gdb.First(&fresh, "id = ?", tk.ID)
	if fresh.Phase != "ready-for-review" {
		t.Fatalf("real repair ended at %s", fresh.Phase)
	}
	content, err := os.ReadFile(filepath.Join(wt, "assertion.txt"))
	if err != nil || string(content) != "fixed\n" {
		t.Fatalf("repair missing: %q %v", content, err)
	}
	if !adapter.sawFeedback {
		t.Fatal("repair did not receive diagnosis feedback")
	}
	if fresh.CheckpointSHA == nil || *fresh.CheckpointSHA == sha {
		t.Fatal("repaired head not checkpointed")
	}
}

type repairAdapter struct {
	*agentrunner.Mock
	t           *testing.T
	worktree    string
	sawFeedback bool
}

func (a *repairAdapter) RunPhase(_ context.Context, _, prompt string, out io.Writer, _ string) error {
	a.sawFeedback = strings.Contains(prompt, "fix the missing assertion")
	if err := os.WriteFile(filepath.Join(a.worktree, "assertion.txt"), []byte("fixed\n"), 0644); err != nil {
		return err
	}
	laneGit(a.t, a.worktree, "add", "assertion.txt")
	laneGit(a.t, a.worktree, "commit", "-m", "fix assertion")
	_, err := io.WriteString(out, "repair complete\n")
	return err
}

func TestDecideRouteOnlyForAutonomousTickets(t *testing.T) {
	auto := decideContract(&client.ClaimResponse{DecisionGuidance: "prefer the cheapest option"})
	for _, want := range []string{`"route":"decide"`, "prefer the cheapest option", "weakening a stated invariant"} {
		if !strings.Contains(auto, want) {
			t.Errorf("autonomous contract missing %q", want)
		}
	}
	if !strings.Contains(decideContract(&client.ClaimResponse{}), defaultDecisionGuidance) {
		t.Error("empty guidance must use the built-in default")
	}
	if got := decideContract(&client.ClaimResponse{AskDecisions: true}); got != "" {
		t.Errorf("ask mode changed the diagnoser contract: %q", got)
	}
}
