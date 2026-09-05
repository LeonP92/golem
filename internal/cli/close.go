package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/agentrunner"
	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/graph"
	"github.com/leonp92/golem/internal/soul"
	"github.com/leonp92/golem/internal/ticket"
	"github.com/leonp92/golem/internal/workspace"
)

type soulProposal struct {
	Filename string
	Content  string
}

func parseSoulProposals(output string) []soulProposal {
	var proposals []soulProposal
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "SOUL:") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		proposals = append(proposals, soulProposal{
			Filename: strings.TrimSpace(parts[1]),
			Content:  strings.TrimSpace(parts[2]),
		})
	}
	return proposals
}

func TicketClose(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ticket close", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *id == "" {
		fmt.Fprintln(stderr, "usage: golem ticket close --ticket <id>")
		return 1
	}

	golemDir := filepath.Join(*repo, ".golem")
	cfg, err := config.Load(filepath.Join(golemDir, "config.yaml"))
	if err != nil {
		fmt.Fprintf(stderr, "loading config: %v\n", err)
		return 1
	}
	ticketDir := filepath.Join(golemDir, "tickets", *id)
	s, err := ticket.Load(ticketDir)
	if err != nil {
		fmt.Fprintf(stderr, "loading ticket %s: %v\n", *id, err)
		return 1
	}
	logPath := filepath.Join(ticketDir, "log.jsonl")
	entries, err := blog.ReadAll(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "reading log: %v\n", err)
		return 1
	}

	if candidates := soul.ExtractCandidates(entries); len(candidates) > 0 {
		if err := proposeAndPromoteSoulEntries(cfg, golemDir, s.WorktreePath, logPath, candidates); err != nil {
			fmt.Fprintf(stderr, "proposing soul entries: %v\n", err)
			return 1
		}
	}

	soulDir := filepath.Join(golemDir, "wiki", "soul")
	if err := promoteReviewerSoulEntries(entries, soulDir, logPath); err != nil {
		fmt.Fprintf(stderr, "promoting reviewer soul entries: %v\n", err)
		return 1
	}

	if err := updateGraphAfterClose(*repo, golemDir, s.Branch, logPath); err != nil {
		// non-fatal: log warning and continue
		w, _ := blog.NewWriter(logPath)
		if w != nil {
			w.Append(blog.NewEntry("system", blog.TypeStatus, "WARNING: graph update failed: "+err.Error()))
			w.Close()
		}
	}

	if err := workspace.Remove(*repo, s.WorktreePath, s.Branch); err != nil {
		fmt.Fprintf(stderr, "tearing down worktree: %v\n", err)
		return 1
	}
	s.Phase = ticket.PhaseClosed
	if err := s.Save(ticketDir); err != nil {
		fmt.Fprintf(stderr, "saving ticket: %v\n", err)
		return 1
	}
	return 0
}

func updateGraphAfterClose(repoRoot, golemDir, branch, logPath string) error {
	meta, err := graph.LoadMeta(filepath.Join(golemDir, "index"))
	if err != nil || meta.BaseCommit == "" {
		return nil // no graph built yet; skip silently
	}
	changed, err := changedFiles(repoRoot, meta.BaseCommit)
	if err != nil || len(changed) == 0 {
		return err
	}
	if code := GraphUpdate([]string{"--repo", repoRoot}, os.Stdout, os.Stderr); code != 0 {
		return fmt.Errorf("graph update exited %d", code)
	}
	return nil
}

func promoteReviewerSoulEntries(entries []blog.Entry, soulDir, logPath string) error {
	w, err := blog.NewWriter(logPath)
	if err != nil {
		return err
	}
	defer w.Close()
	for _, e := range entries {
		if e.Role != "reviewer" || e.Type != blog.TypeStatus {
			continue
		}
		for _, p := range parseSoulProposals(e.Message) {
			if err := soul.Promote(soulDir, p.Filename, p.Content); err != nil {
				return fmt.Errorf("promoting %s: %w", p.Filename, err)
			}
			w.Append(blog.NewEntry("reviewer", blog.TypeStatus, "promoted soul entry: "+p.Filename))
		}
	}
	return nil
}

func proposeAndPromoteSoulEntries(cfg *config.Config, golemDir, worktreePath, logPath string, candidates []soul.Candidate) error {
	rolePrompt, err := os.ReadFile(filepath.Join(golemDir, "roles", "reviewer.md"))
	if err != nil {
		return err
	}
	runner, err := NewRunner(cfg, worktreePath)
	if err != nil {
		return err
	}

	var divergences strings.Builder
	for _, c := range candidates {
		fmt.Fprintf(&divergences, "- %s\n", c.Suggestion)
	}
	prompt := string(rolePrompt) + "\n\n## Divergences to consider from this ticket\n" + divergences.String()

	result, err := runner.RunAgent("reviewer", agentrunner.Context{RolePrompt: prompt})
	if err != nil {
		return err
	}

	w, err := blog.NewWriter(logPath)
	if err != nil {
		return err
	}
	defer w.Close()

	soulDir := filepath.Join(golemDir, "wiki", "soul")
	for _, p := range parseSoulProposals(result.Output) {
		if err := soul.Promote(soulDir, p.Filename, p.Content); err != nil {
			return fmt.Errorf("promoting %s: %w", p.Filename, err)
		}
		if err := w.Append(blog.NewEntry("reviewer", blog.TypeStatus, "promoted soul entry: "+p.Filename)); err != nil {
			return err
		}
	}
	return nil
}
