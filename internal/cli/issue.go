package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/ticket"
)

// IssueList prints open issues carrying the configured trigger label.
//
// This never writes to GitHub: it only calls the read-only ListIssuesSince
// endpoint. cfg.GitHub.Write is not consulted here — no command in this file
// calls a GitHub write endpoint yet. Write exists so golem init (standalone
// use) and the shem's ensureRepoReady (orchestrator-managed repos) agree on
// which side is allowed to become a GitHub writer once a write-capable
// command is added; see internal/shem/worker/executor.go's setGitHubWrite.
func IssueList(gh github.Client, cfg *config.Config, out io.Writer) error {
	owner, name, err := splitRepo(cfg.GitHub.Repo)
	if err != nil {
		return err
	}
	page, err := gh.ListIssuesSince(context.Background(), owner, name, cfg.GitHub.Label, time.Time{}, "")
	if err != nil {
		return fmt.Errorf("listing issues for %s/%s: %w", owner, name, err)
	}
	for _, i := range page.Issues {
		if i.State != "open" {
			continue
		}
		fmt.Fprintf(out, "#%d\t%s\n", i.Number, i.Title)
	}
	return nil
}

// IssueSync pulls the linked issue's current title and body onto the ticket:
// the ticket description is overwritten with the issue body (GitHub is the
// source of truth) and issue_url is refreshed. All other state.json fields
// are preserved because this loads and saves through ticket.State rather
// than editing raw JSON.
//
// This never writes to GitHub: it only calls the read-only GetIssue
// endpoint. See IssueList's doc comment for why cfg.GitHub.Write is not
// consulted here.
func IssueSync(gh github.Client, cfg *config.Config, ticketDir string) error {
	owner, name, err := splitRepo(cfg.GitHub.Repo)
	if err != nil {
		return err
	}
	s, err := ticket.Load(ticketDir)
	if err != nil {
		return fmt.Errorf("loading ticket state: %w", err)
	}
	if s.IssueNumber == 0 {
		return fmt.Errorf("ticket is not linked to a GitHub issue")
	}
	issue, err := gh.GetIssue(context.Background(), owner, name, s.IssueNumber)
	if err != nil {
		return fmt.Errorf("fetching issue #%d: %w", s.IssueNumber, err)
	}
	s.Description = issue.Body
	s.IssueURL = issue.HTMLURL
	if err := s.Save(ticketDir); err != nil {
		return fmt.Errorf("saving ticket state: %w", err)
	}
	return nil
}

// resolveFromIssue fetches issue number n from cfg.GitHub.Repo and returns
// the fields needed to seed a new ticket from it. Split out from TicketNew
// so it is testable against github.NewFake() without a real token or
// network access.
func resolveFromIssue(gh github.Client, cfg *config.Config, n int) (description string, issueNumber int, issueURL string, err error) {
	owner, name, err := splitRepo(cfg.GitHub.Repo)
	if err != nil {
		return "", 0, "", err
	}
	issue, err := gh.GetIssue(context.Background(), owner, name, n)
	if err != nil {
		return "", 0, "", fmt.Errorf("fetching issue #%d: %w", n, err)
	}
	return issue.Title, issue.Number, issue.HTMLURL, nil
}

// splitRepo parses an "org/repo" string into its parts.
func splitRepo(repo string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("github.repo must be set to \"org/repo\" in .golem/config.yaml")
	}
	return parts[0], parts[1], nil
}

// newGitHubClient builds a GitHub client from GOLEM_GITHUB_TOKEN. Golem does
// not invent an alternate token flow (keychains, gh CLI config, etc.) — the
// environment variable is the only supported source, and its absence fails
// fast with a clear message rather than an opaque 401 from GitHub.
func newGitHubClient() (github.Client, error) {
	token := os.Getenv("GOLEM_GITHUB_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("GOLEM_GITHUB_TOKEN is not set; GitHub issue commands require it")
	}
	return github.New(token, "")
}

// IssueListCmd is the `golem issue list` command.
func IssueListCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("issue list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	cfg, err := config.Load(filepath.Join(*repo, ".golem", "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	gh, err := newGitHubClient()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := IssueList(gh, cfg, stdout); err != nil {
		fmt.Fprintf(stderr, "listing issues: %v\n", err)
		return 1
	}
	return 0
}

// IssueSyncCmd is the `golem issue sync` command.
func IssueSyncCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("issue sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	ticketID := fs.String("ticket", "", "ticket id to sync (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *ticketID == "" {
		fmt.Fprintln(stderr, "usage: golem issue sync --ticket <id>")
		return 1
	}

	cfg, err := config.Load(filepath.Join(*repo, ".golem", "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	gh, err := newGitHubClient()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ticketDir := filepath.Join(*repo, ".golem", "tickets", *ticketID)
	if err := IssueSync(gh, cfg, ticketDir); err != nil {
		fmt.Fprintf(stderr, "syncing issue: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "synced ticket %s from its linked issue\n", *ticketID)
	return 0
}
