package worker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// recordedPhase is one RunPhase call the recording adapter saw.
type recordedPhase struct {
	dir    string
	prompt string
	model  string
}

// recordingAdapter answers every phase and records what it was asked for.
type recordingAdapter struct {
	agentrunner.Adapter
	phases *[]recordedPhase
}

func (recordingAdapter) Name() string           { return "recording" }
func (recordingAdapter) ReservedArgs() []string { return nil }

func (r recordingAdapter) RunPhase(_ context.Context, dir, prompt string, out io.Writer, model string) error {
	*r.phases = append(*r.phases, recordedPhase{dir: dir, prompt: prompt, model: model})
	_, err := io.WriteString(out, "recorded\n")
	return err
}

func (recordingAdapter) WorktreeSetup(string) error                        { return nil }
func (recordingAdapter) PrepareHost(string) error                          { return nil }
func (recordingAdapter) GenerateArtifacts(string, map[string]string) error { return nil }

// recordedPhases is filled by the adapter registered below. Tests in this
// package run sequentially and each clears it.
var recordedPhases []recordedPhase

func init() {
	agentrunner.Register("recording", func(agentrunner.Options) agentrunner.Adapter {
		return recordingAdapter{phases: &recordedPhases}
	})
}

// testCatalog is a three-tier selectable catalog with one model per tier.
func testCatalog() models.Catalog {
	return models.Catalog{
		SupportsSelection: true,
		Tiers:             []models.Tier{"small", "medium", "large"},
		Models: []models.Model{
			{ID: "small-model", Tier: "small"},
			{ID: "mid-model", Tier: "medium"},
			{ID: "big-model", Tier: "large"},
		},
		StageDefaults: map[models.Stage]models.Tier{
			models.StageBrainstorm: "large", models.StagePlan: "large",
			models.StageImplement: "medium", models.StageRevise: "medium",
			models.StageValidate: "medium", models.StageObserve: "small",
			models.StageReview: "large", models.StagePRDescription: "small",
			models.StageGraph: "small",
		},
	}
}

// testAgent builds an Agent over the recording adapter and the test catalog.
func testAgent(t *testing.T) *Agent {
	t.Helper()
	recordedPhases = nil
	backend := agentrunner.BackendConfig{Adapter: "recording", Catalog: testCatalog()}
	cfg := &config.Config{Backend: &backend}
	agent, cleanup, err := NewAgent(cfg)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	t.Cleanup(cleanup)
	return agent
}

// testExecutor is a GolemExecutor wired to the recording adapter.
func testExecutor(t *testing.T) *GolemExecutor {
	t.Helper()
	return &GolemExecutor{Agent: testAgent(t)}
}

func TestNewAgentWritesAReadableBackendDocument(t *testing.T) {
	backend := agentrunner.SeedClaudeCode()
	agent, cleanup, err := NewAgent(&config.Config{Backend: &backend})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	info, err := os.Stat(agent.ConfigPath)
	if err != nil {
		t.Fatalf("stat the backend document: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("backend.yaml mode = %v, want 0644", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(agent.ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	// The agent account runs the nested subcommands and has to enter this.
	if dirInfo.Mode().Perm() != 0o755 {
		t.Errorf("run directory mode = %v, want 0755", dirInfo.Mode().Perm())
	}
	back, err := agentrunner.LoadBackendConfig(agent.ConfigPath)
	if err != nil {
		t.Fatalf("the written document does not load: %v", err)
	}
	if back.Adapter != "claude-code" || len(back.Models) != len(backend.Models) {
		t.Errorf("round trip gave %+v", back)
	}

	cleanup()
	if _, err := os.Stat(filepath.Dir(agent.ConfigPath)); err == nil {
		t.Error("cleanup left the run directory behind")
	}
}

func TestAgentStageModelResolvesEveryStage(t *testing.T) {
	agent := testAgent(t)
	want := map[models.Stage]string{
		models.StageBrainstorm: "big-model", models.StagePlan: "big-model",
		models.StageImplement: "mid-model", models.StageRevise: "mid-model",
		models.StageValidate: "mid-model", models.StageObserve: "small-model",
		models.StageReview: "big-model", models.StagePRDescription: "small-model",
		models.StageGraph: "small-model",
	}
	for _, st := range models.Stages {
		if got := agent.StageModel(st); got != want[st] {
			t.Errorf("StageModel(%q) = %q, want %q", st, got, want[st])
		}
	}
}

func TestSubcommandArgsCarriesTheBackendDocumentAndModel(t *testing.T) {
	agent := testAgent(t)
	tests := []struct {
		name  string
		model string
		want  []string
	}{
		{"with a model", "big-model", []string{"--backend-config", agent.ConfigPath, "--model", "big-model"}},
		{"vendor default", "", []string{"--backend-config", agent.ConfigPath}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agent.subcommandArgs([]string{"ticket", "review"}, tt.model)
			want := append([]string{"ticket", "review"}, tt.want...)
			if strings.Join(got, " ") != strings.Join(want, " ") {
				t.Errorf("args = %q, want %q", got, want)
			}
		})
	}
}

func TestRunPhaseUsesTheAdapterAndNamesTheLogAfterThePhase(t *testing.T) {
	e := testExecutor(t)
	dir := t.TempDir()
	if err := e.runPhase(context.Background(), "/repo", "a prompt", dir, "brainstorm", "big-model"); err != nil {
		t.Fatalf("runPhase: %v", err)
	}
	if len(recordedPhases) != 1 {
		t.Fatalf("%d phases ran, want 1", len(recordedPhases))
	}
	got := recordedPhases[0]
	if got.dir != "/repo" || got.prompt != "a prompt" || got.model != "big-model" {
		t.Errorf("the adapter saw %+v", got)
	}
	// The log is vendor-neutral now, not claude-<phase>.log.
	data, err := os.ReadFile(filepath.Join(dir, "agent-brainstorm.log"))
	if err != nil {
		t.Fatalf("read the phase log: %v", err)
	}
	if !strings.Contains(string(data), "recorded") {
		t.Errorf("the phase log does not carry the agent's output: %q", data)
	}
}

// logCapture is an orchestrator stub collecting posted log entries.
func logCapture(t *testing.T) (*client.Client, *[]client.LogPayload) {
	t.Helper()
	var posted []client.LogPayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p client.LogPayload
		json.NewDecoder(r.Body).Decode(&p) //nolint:errcheck
		posted = append(posted, p)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"sequence_num": len(posted)}) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "k", "test-shem")
	c.RetryInitial = time.Millisecond
	return c, &posted
}

func TestSanitizeModelPassesWhatTheAdapterCanUse(t *testing.T) {
	c, _ := logCapture(t)
	for _, model := range []string{"big-model", ""} {
		if got := sanitizeModel(c, "ticket-1", model); got != model {
			t.Errorf("sanitizeModel(%q) = %q, want it unchanged", model, got)
		}
	}
}

// The shem is downstream of an orchestrator that may be newer than it, so a
// value it cannot use is reported rather than a reason to strand the ticket.
func TestARejectedModelWarnsAndFallsBack(t *testing.T) {
	for _, model := range []string{"--model", "a b", "-p"} {
		c, posted := logCapture(t)
		if got := sanitizeModel(c, "ticket-1", model); got != "" {
			t.Errorf("sanitizeModel(%q) = %q, want the vendor default", model, got)
		}
		if len(*posted) != 1 || (*posted)[0].EntryType != "WARNING" {
			t.Fatalf("posted = %+v, want one WARNING", *posted)
		}
		if !strings.Contains((*posted)[0].Message, model) {
			t.Errorf("the warning %q does not name %q", (*posted)[0].Message, model)
		}
	}
}

func TestPhaseStartLogsTheBackendAndModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		shown string
	}{
		{"a selected model", "big-model", "model=big-model"},
		{"no selection", "", "model=vendor default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, posted := logCapture(t)
			postPhaseStart(c, "ticket-1", "recording", tt.model, "brainstorm")
			if len(*posted) != 1 {
				t.Fatalf("posted = %+v, want one entry", *posted)
			}
			got := (*posted)[0]
			if got.Backend != "recording" {
				t.Errorf("Backend = %q, want recording", got.Backend)
			}
			if got.Model != tt.model {
				t.Errorf("Model = %q, want %q", got.Model, tt.model)
			}
			if !strings.Contains(got.Message, tt.shown) {
				t.Errorf("message %q does not carry %q", got.Message, tt.shown)
			}
		})
	}
}

// The shem hands golem init its own adapter, not whatever the repository's
// config.yaml happens to name.
func TestGolemInitUsesTheShemsOwnAdapter(t *testing.T) {
	e := testExecutor(t)
	if got := e.Agent.Adapter.Name(); got != "recording" {
		t.Fatalf("adapter = %q, want recording", got)
	}
	repo := t.TempDir()
	calls, _ := fakeGolemOnPath(t)
	_ = e.ensureRepoReady(context.Background(), repo)

	lines := strings.Join(readLines(t, calls), "\n")
	if !strings.Contains(lines, "init --backend recording") {
		t.Errorf("golem init did not carry the shem's adapter:\n%s", lines)
	}
}

// Every agent-invoking subcommand carries the backend document, and
// `ticket advance` — which invokes none — does not.
func TestSubcommandsCarryTheBackendDocument(t *testing.T) {
	e := testExecutor(t)
	repo := t.TempDir()
	calls, _ := fakeGolemOnPath(t)
	c, _ := logCapture(t)
	ctx := context.Background()

	if err := e.runGolemValidate(ctx, c, repo, "t-1", "spec"); err != nil {
		t.Logf("validate: %v", err)
	}
	if err := e.runGolemReview(ctx, c, repo, "t-1"); err != nil {
		t.Logf("review: %v", err)
	}
	if _, err := e.generatePRDescription(ctx, repo, "t-1"); err != nil {
		t.Logf("pr-description: %v", err)
	}
	if err := e.runGolemTicketNew(ctx, repo, "t-1", "b", "a description"); err != nil {
		t.Logf("ticket new: %v", err)
	}
	if err := runGolemAdvance(ctx, repo, "t-1", "plan"); err != nil {
		t.Logf("advance: %v", err)
	}

	lines := readLines(t, calls)
	want := map[string]string{
		"ticket validate":       "--model mid-model",
		"ticket review":         "--model big-model",
		"ticket pr-description": "--model small-model",
		"ticket new":            "",
	}
	for prefix, model := range want {
		var found string
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				found = l
			}
		}
		if found == "" {
			t.Errorf("%q was never run: %v", prefix, lines)
			continue
		}
		if !strings.Contains(found, "--backend-config "+e.Agent.ConfigPath) {
			t.Errorf("%q does not carry the backend document: %q", prefix, found)
		}
		if model == "" {
			if strings.Contains(found, "--model") {
				t.Errorf("%q carries a model but invokes no agent: %q", prefix, found)
			}
			continue
		}
		if !strings.Contains(found, model) {
			t.Errorf("%q does not carry %q: %q", prefix, model, found)
		}
	}
	for _, l := range lines {
		if strings.HasPrefix(l, "ticket advance") && strings.Contains(l, "--backend-config") {
			t.Errorf("ticket advance invokes no agent but carries the backend document: %q", l)
		}
	}
}

// The graph subcommands take both flags on every path, including the rebuild
// fallback.
func TestGraphSubcommandsCarryTheBackendDocument(t *testing.T) {
	e := testExecutor(t)
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".golem", "index"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".golem", "config.yaml"),
		[]byte("backend: claude-code\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A golem that records its argv and then fails, so the rebuild fallback
	// runs too.
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.txt")
	writeFakeGolem(t, dir, "#!/bin/sh\necho \"$@\" >> "+calls+"\nexit 1\n")
	_ = e.ensureRepoReady(context.Background(), repo)

	lines := readLines(t, calls)
	var graphCalls []string
	for _, l := range lines {
		if strings.HasPrefix(l, "graph ") {
			graphCalls = append(graphCalls, l)
		}
	}
	// update, then the rebuild fallback.
	if len(graphCalls) != 2 {
		t.Fatalf("graph calls = %v, want update then build", graphCalls)
	}
	for _, l := range graphCalls {
		if !strings.Contains(l, "--backend-config "+e.Agent.ConfigPath) {
			t.Errorf("%q does not carry the backend document", l)
		}
		if !strings.Contains(l, "--model small-model") {
			t.Errorf("%q does not carry the graph stage's model", l)
		}
	}
}
