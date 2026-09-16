package cli

import (
	"context"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/github"
	odb "github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
)

// TestCLIAndOrchestratorAgreeOnTheDescription measures the parity goal rather
// than inferring it: both real code paths are run against one shared
// github.Issue value and the resulting description bytes are compared.
//
// This is the whole point of routing both through
// github.Issue.TicketDescription. They diverged before — the CLI combined
// title and body while ingest stored the body alone (46 bytes against 24 on
// the case below) — which meant an orchestrator agent was never shown an
// issue's title, and a title-only issue became an empty task.
//
// The test lives in internal/cli, as an internal test, so it can call the
// unexported resolveFromIssue that `golem ticket new --from-issue` uses. It
// imports the orchestrator packages only to drive the other real path; there
// is no dependency the other way.
func TestCLIAndOrchestratorAgreeOnTheDescription(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
	}{
		{
			name:  "title and body",
			title: "Crash on empty input",
			body:  "Steps:\n1. run it\n2. boom",
		},
		{
			name:  "title only, empty body",
			title: "Add a --json flag to `golem tickets`",
			body:  "",
		},
		{
			name:  "body containing blank lines",
			title: "Flaky test",
			body:  "first\n\nsecond\n\nthird",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			issue := github.Issue{
				Number: 7, Title: tc.title, Body: tc.body, State: "open",
				HTMLURL:   "https://github.com/org/repo/issues/7",
				UpdatedAt: time.Now(), Labels: []string{"golem"},
			}

			cliDesc := cliDescriptionFor(t, issue)
			orcDesc, orcHash := orchestratorDescriptionFor(t, issue)

			if cliDesc != orcDesc {
				t.Errorf("descriptions differ:\n  CLI          = %q (%d bytes)\n  ORCHESTRATOR = %q (%d bytes)",
					cliDesc, len(cliDesc), orcDesc, len(orcDesc))
			}
			// The orchestrator additionally has to hash what it stored, or
			// the approval gate refuses every approval.
			if orcHash != ghsync.HashDescription(orcDesc) {
				t.Error("the orchestrator's body_hash does not hash its own description")
			}
		})
	}
}

// cliDescriptionFor runs the real `--from-issue` resolution path.
func cliDescriptionFor(t *testing.T, issue github.Issue) string {
	t.Helper()
	f := github.NewFake()
	f.Default = "main"
	f.AddIssue(issue)
	cfg := &config.Config{Backend: "claude-code"}
	cfg.GitHub.Repo = "org/repo"
	cfg.GitHub.Label = "golem"
	desc, _, _, err := resolveFromIssue(f, cfg, issue.Number)
	if err != nil {
		t.Fatalf("resolveFromIssue: %v", err)
	}
	return desc
}

// orchestratorDescriptionFor runs a real ghsync ingest pass and returns the
// description and body_hash it stored.
func orchestratorDescriptionFor(t *testing.T, issue github.Issue) (string, string) {
	t.Helper()
	gdb, err := odb.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	repo := odb.GitHubRepo{
		RepoRemote: "https://github.com/org/repo",
		Owner:      "org", Name: "repo", Enabled: true, Label: "golem",
	}
	if err := gdb.Create(&repo).Error; err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	f := github.NewFake()
	f.Default = "main"
	f.AddIssue(issue)
	if err := ghsync.NewSyncer(gdb, f).IngestRepo(context.Background(), &repo); err != nil {
		t.Fatalf("IngestRepo: %v", err)
	}
	var ticket odb.Ticket
	if err := gdb.First(&ticket).Error; err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	return ticket.Description, ticket.BodyHash
}
