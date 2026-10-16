package cli

import (
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/ticket"
	"github.com/leonp92/golem/internal/workspace"
)

func TicketNew(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("id", "", "ticket id (required)")
	ticketID := fs.String("ticket-id", "", "ticket id assigned by orchestrator (alternative to --id)")
	branch := fs.String("branch", "", "working branch name (optional; defaults to ticket/<id> for standalone use)")
	trivial := fs.Bool("trivial", false, "skip brainstorm, go straight to plan with developer+reviewer only")
	fromIssue := fs.Int("from-issue", 0, "create the ticket from GitHub issue <n>, using its title and body as the description")
	backendConfig := fs.String("backend-config", "", "path to a backend config document")
	backend := fs.String("backend", "", "registered adapter name")
	base := fs.String("base", "", "git ref the worktree is created from (e.g. origin/main); resolved and pinned to an exact commit")
	baseSHA := fs.String("base-sha", "", "already-resolved commit the worktree is created from; verified to exist before use")
	var accept stringList
	fs.Var(&accept, "accept", "acceptance command the gate runs after gate.commands (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *ticketID != "" {
		*id = *ticketID
	}
	description := strings.Join(fs.Args(), " ")
	if *id == "" || (description == "" && *fromIssue == 0) {
		fmt.Fprintln(stderr, "usage: golem ticket new (--id | --ticket-id) <id> (<description> | --from-issue <n>)")
		fmt.Fprintln(stderr, "  --id: ticket id (can be omitted for human users, required for orchestrator)")
		fmt.Fprintln(stderr, "  --ticket-id: alias for --id used by Shem workers; if both provided, --ticket-id wins")
		fmt.Fprintln(stderr, "  --from-issue: fetch the description from GitHub issue <n> instead of a positional argument")
		return 1
	}

	cfg, err := config.Load(filepath.Join(*repo, ".golem", "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}

	var issueNumber int
	var issueURL string
	if *fromIssue > 0 {
		gh, err := newGitHubClient()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		desc, n, url, err := resolveFromIssue(gh, cfg, *fromIssue)
		if err != nil {
			fmt.Fprintf(stderr, "resolving --from-issue %d: %v\n", *fromIssue, err)
			return 1
		}
		description = desc
		issueNumber = n
		issueURL = url
	}
	if description == "" {
		fmt.Fprintf(stderr, "issue #%d has no title or body, cannot use it as the ticket description\n", *fromIssue)
		return 1
	}

	runner, err := NewRunner(RunnerSource{
		Config: cfg, WorktreeRoot: *repo, BackendConfig: *backendConfig, Backend: *backend,
	})
	if err != nil {
		fmt.Fprintf(stderr, "initialising runner: %v\n", err)
		return 1
	}

	workingBranch := *branch
	if workingBranch == "" {
		workingBranch = "ticket/" + *id
	}
	s := ticket.New(*id, description, *trivial)
	if issueNumber != 0 {
		s.IssueNumber = issueNumber
		s.IssueURL = issueURL
	}
	// The base is pinned to an exact commit, never left as a floating ref:
	// "origin/main" moves between the resolve and the checkout often enough
	// that a ticket can silently start one commit behind the base its
	// reviewers and predecessors assume. --base-sha exists for callers that
	// already pinned the commit themselves (the shem resolves origin/<base>
	// after fetching); it is verified here all the same, because the
	// worktree is only as reproducible as the commit it starts from.
	baseRef := "HEAD"
	pinnedSHA := ""
	if *base != "" || *baseSHA != "" {
		resolve := *base
		if *baseSHA != "" {
			resolve = *baseSHA
		}
		sha, err := resolveCommitSHA(*repo, resolve)
		if err != nil {
			fmt.Fprintf(stderr, "resolving worktree base: %v\n", err)
			return 1
		}
		baseRef, pinnedSHA = sha, sha
	}
	worktreePath, err := workspace.Create(*repo, *id, workingBranch, baseRef)
	if err != nil {
		fmt.Fprintf(stderr, "creating worktree: %v\n", err)
		return 1
	}
	if err := runner.WorktreeSetup(worktreePath); err != nil {
		fmt.Fprintf(stderr, "worktree setup: %v\n", err)
		return 1
	}
	s.Branch = workingBranch
	s.WorktreePath = worktreePath
	s.BranchBaseSHA = pinnedSHA
	s.AcceptanceCommands = accept

	ticketDir := filepath.Join(*repo, ".golem", "tickets", *id)
	if err := s.Save(ticketDir); err != nil {
		fmt.Fprintf(stderr, "saving ticket state: %v\n", err)
		return 1
	}

	w, err := blog.NewWriter(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		fmt.Fprintf(stderr, "opening ticket log: %v\n", err)
		return 1
	}
	defer closeLogWriter(w, stderr)
	if err := w.Append(blog.NewEntry("system", blog.TypeStatus, "ticket created: "+description)); err != nil {
		fmt.Fprintf(stderr, "writing initial log entry: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "created ticket %s on branch %s\n", *id, workingBranch)
	return 0
}

// resolveCommitSHA verifies that ref names a commit in repo and returns its
// full SHA. The ^{commit} peel keeps an annotated tag or a non-commit ref
// from passing the check by accident.
func resolveCommitSHA(repo, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", ref+"^{commit}")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%q does not resolve to a commit: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ", ") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }
