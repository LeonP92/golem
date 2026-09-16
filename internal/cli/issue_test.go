package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/ticket"
)

func TestIssueListPrintsLabeledIssues(t *testing.T) {
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "Add rate limiting", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})
	f.AddIssue(github.Issue{Number: 9, Title: "Unrelated", State: "open",
		UpdatedAt: time.Now(), Labels: []string{"bug"}})

	cfg := &config.Config{Backend: "claude-code",
		GitHub: config.GitHubConfig{Repo: "org/repo", Label: "golem"}}

	var out bytes.Buffer
	if err := IssueList(f, cfg, &out); err != nil {
		t.Fatalf("IssueList: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "#7") || !strings.Contains(got, "Add rate limiting") {
		t.Errorf("labeled issue missing from output: %s", got)
	}
	if strings.Contains(got, "#9") {
		t.Errorf("unlabeled issue listed: %s", got)
	}
}

func TestIssueListSkipsClosedIssues(t *testing.T) {
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 3, Title: "Closed one", State: "closed",
		UpdatedAt: time.Now(), Labels: []string{"golem"}})

	cfg := &config.Config{Backend: "claude-code",
		GitHub: config.GitHubConfig{Repo: "org/repo", Label: "golem"}}

	var out bytes.Buffer
	if err := IssueList(f, cfg, &out); err != nil {
		t.Fatalf("IssueList: %v", err)
	}
	if got := out.String(); got != "" {
		t.Errorf("expected no output for a closed issue, got: %s", got)
	}
}

func TestIssueListRequiresRepo(t *testing.T) {
	cfg := &config.Config{Backend: "claude-code"}
	var out bytes.Buffer
	if err := IssueList(github.NewFake(), cfg, &out); err == nil {
		t.Fatal("IssueList succeeded with no repo configured, want an error")
	}
}

func TestIssueSyncPullsBodyAndURLPreservingOtherFields(t *testing.T) {
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 7, Title: "Add rate limiting", Body: "Do the thing",
		State: "open", HTMLURL: "https://github.test/org/repo/issues/7", UpdatedAt: time.Now()})

	cfg := &config.Config{Backend: "claude-code", GitHub: config.GitHubConfig{Repo: "org/repo"}}

	ticketDir := t.TempDir()
	s := ticket.New("t1", "stale description", false)
	s.Branch = "ticket/t1"
	s.WorktreePath = "/some/worktree"
	s.IssueNumber = 7
	if err := s.Save(ticketDir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := IssueSync(f, cfg, ticketDir); err != nil {
		t.Fatalf("IssueSync: %v", err)
	}

	got, err := ticket.Load(ticketDir)
	if err != nil {
		t.Fatalf("ticket.Load: %v", err)
	}
	if got.Description != "Do the thing" {
		t.Errorf("Description = %q, want the issue body", got.Description)
	}
	if got.IssueURL != "https://github.test/org/repo/issues/7" {
		t.Errorf("IssueURL = %q, want the issue's HTML URL", got.IssueURL)
	}
	// Fields unrelated to the issue sync must survive untouched.
	if got.Branch != "ticket/t1" || got.WorktreePath != "/some/worktree" || got.IssueNumber != 7 {
		t.Errorf("unrelated fields not preserved: %+v", got)
	}
}

func TestIssueSyncRequiresLinkedIssue(t *testing.T) {
	cfg := &config.Config{Backend: "claude-code", GitHub: config.GitHubConfig{Repo: "org/repo"}}
	ticketDir := t.TempDir()
	s := ticket.New("t1", "no issue linked", false)
	if err := s.Save(ticketDir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := IssueSync(github.NewFake(), cfg, ticketDir); err == nil {
		t.Fatal("IssueSync succeeded on a ticket with no linked issue, want an error")
	}
}

func TestResolveFromIssueReturnsTitleAndLink(t *testing.T) {
	f := github.NewFake()
	f.AddIssue(github.Issue{Number: 12, Title: "Fix the flaky test",
		HTMLURL: "https://github.test/org/repo/issues/12", State: "open"})
	cfg := &config.Config{Backend: "claude-code", GitHub: config.GitHubConfig{Repo: "org/repo"}}

	desc, number, url, err := resolveFromIssue(f, cfg, 12)
	if err != nil {
		t.Fatalf("resolveFromIssue: %v", err)
	}
	if desc != "Fix the flaky test" {
		t.Errorf("description = %q, want the issue title", desc)
	}
	if number != 12 {
		t.Errorf("issueNumber = %d, want 12", number)
	}
	if url != "https://github.test/org/repo/issues/12" {
		t.Errorf("issueURL = %q, want the issue's HTML URL", url)
	}
}

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		wantErr bool
	}{
		{"valid", "org/repo", false},
		{"empty", "", true},
		{"no slash", "orgrepo", true},
		{"too many parts", "org/repo/extra", true},
		{"empty owner", "/repo", true},
		{"empty name", "org/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, name, err := splitRepo(tt.repo)
			if (err != nil) != tt.wantErr {
				t.Fatalf("splitRepo(%q) error = %v, wantErr %v", tt.repo, err, tt.wantErr)
			}
			if !tt.wantErr && (owner == "" || name == "") {
				t.Errorf("splitRepo(%q) = %q, %q, want non-empty parts", tt.repo, owner, name)
			}
		})
	}
}

func TestNewGitHubClientRequiresToken(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "")
	if _, err := newGitHubClient(); err == nil {
		t.Fatal("newGitHubClient succeeded with no token set, want an error")
	}
}

func TestNewGitHubClientAcceptsToken(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "test-token")
	if _, err := newGitHubClient(); err != nil {
		t.Fatalf("newGitHubClient: %v", err)
	}
}

func TestIssueListCmdFailsWithoutToken(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "")
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer
	code := IssueListCmd([]string{"--repo", repo}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit with no token")
	}
	if !strings.Contains(stderr.String(), "GOLEM_GITHUB_TOKEN") {
		t.Errorf("expected clear message about the missing token, got: %s", stderr.String())
	}
}

func TestIssueSyncCmdRequiresTicketFlag(t *testing.T) {
	t.Setenv("GOLEM_GITHUB_TOKEN", "test-token")
	repo := initRepoForCLI(t)
	var stdout, stderr bytes.Buffer
	code := IssueSyncCmd([]string{"--repo", repo}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit with no --ticket flag")
	}
}

func TestGolemInitConfigDefaultsGitHubWriteTrue(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Init([]string{"--repo", dir, "--backend", "claude-code"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Init failed: exit code %d, stderr=%s", code, stderr.String())
	}
	cfg, err := config.Load(filepath.Join(dir, ".golem", "config.yaml"))
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if !cfg.GitHub.Write {
		t.Error("golem init must default github.write to true for standalone CLI use")
	}
}

// Ensure state.json round-trips through os.ReadFile cleanly (sanity check that
// omitempty doesn't break unlinked tickets).
func TestTicketStateWithoutIssueLinkOmitsFields(t *testing.T) {
	dir := t.TempDir()
	s := ticket.New("t1", "no issue", false)
	if err := s.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "issue_number") || strings.Contains(string(data), "issue_url") {
		t.Errorf("expected issue fields to be omitted when unset, got: %s", data)
	}
}
