package ghsync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/leonp92/golem/internal/github"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/slug"
	"gorm.io/gorm"
)

// cursorOverlap is subtracted from the newest issue timestamp when advancing
// the sync cursor, so clock skew between GitHub and the orchestrator cannot
// skip an issue. The resulting re-reads are harmless: creates are blocked by
// the unique index on (repo_remote, issue_number) and updates are idempotent.
const cursorOverlap = time.Minute

// Syncer carries the dependencies shared by ingest, publish, and reconcile.
type Syncer struct {
	DB *gorm.DB
	GH github.Client
}

// NewSyncer returns a Syncer over the given database and GitHub client.
func NewSyncer(gdb *gorm.DB, client github.Client) *Syncer {
	return &Syncer{DB: gdb, GH: client}
}

// IngestRepo pulls issues changed since the repo's cursor and reconciles them
// into tickets. GitHub is the source of truth for Title and Description;
// Phase, Branch, and BaseBranch are Golem-owned and never overwritten here,
// except that a closed issue moves its ticket to phase "closed".
func (s *Syncer) IngestRepo(ctx context.Context, repo *db.GitHubRepo) error {
	since := time.Time{}
	if repo.LastIssueSync != nil {
		since = *repo.LastIssueSync
	}

	page, err := s.GH.ListIssuesSince(ctx, repo.Owner, repo.Name, repo.Label, since, repo.ETag)
	if err != nil {
		s.recordRepoError(repo, err)
		return fmt.Errorf("list issues for %s: %w", repo.RepoRemote, err)
	}

	now := time.Now()
	if page.NotModified {
		if err := s.DB.Model(repo).Updates(map[string]any{
			"last_polled_at": now, "last_error": "",
		}).Error; err != nil {
			return fmt.Errorf("record not-modified poll for %s: %w", repo.RepoRemote, err)
		}
		return nil
	}

	newest := since
	for _, issue := range page.Issues {
		if err := s.applyIssue(ctx, repo, issue); err != nil {
			// One bad issue must not abandon the rest of the page.
			log.Printf("ghsync: repo %s issue #%d: %v", repo.RepoRemote, issue.Number, err)
			continue
		}
		if issue.UpdatedAt.After(newest) {
			newest = issue.UpdatedAt
		}
	}

	updates := map[string]any{"last_polled_at": now, "last_error": ""}
	if page.ETag != "" {
		updates["etag"] = page.ETag
	}
	if newest.After(since) {
		cursor := newest.Add(-cursorOverlap)
		updates["last_issue_sync"] = cursor
		repo.LastIssueSync = &cursor
	}
	if err := s.DB.Model(repo).Updates(updates).Error; err != nil {
		return fmt.Errorf("record poll result for %s: %w", repo.RepoRemote, err)
	}
	return nil
}

// applyIssue creates or updates the ticket linked to one issue.
func (s *Syncer) applyIssue(ctx context.Context, repo *db.GitHubRepo, issue github.Issue) error {
	var ticket db.Ticket
	err := s.DB.Where("repo_remote = ? AND issue_number = ?", repo.RepoRemote, issue.Number).
		First(&ticket).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		if issue.State != "open" {
			// Golem does not resurrect issues closed before it saw them.
			return nil
		}
		return s.createTicketFromIssue(ctx, repo, issue)
	}
	if err != nil {
		return fmt.Errorf("look up ticket for issue #%d: %w", issue.Number, err)
	}

	updates := map[string]any{
		"title":       issue.Title,
		"description": issue.Body,
		"issue_url":   issue.HTMLURL,
	}
	if issue.State == "closed" && ticket.Phase != "closed" {
		updates["phase"] = "closed"
	}
	if err := s.DB.Model(&db.Ticket{}).Where("id = ?", ticket.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("update ticket %s for issue #%d: %w", ticket.ID, issue.Number, err)
	}
	return nil
}

// createTicketFromIssue opens a new unassigned ticket for an issue. A shem
// claims it through the existing /api/tickets/available path.
func (s *Syncer) createTicketFromIssue(ctx context.Context, repo *db.GitHubRepo, issue github.Issue) error {
	base, err := s.GH.DefaultBranch(ctx, repo.Owner, repo.Name)
	if err != nil || base == "" {
		base = "main"
	}
	id := uuid.NewString()
	number := issue.Number
	ticket := db.Ticket{
		ID:          id,
		RepoRemote:  repo.RepoRemote,
		BaseBranch:  base,
		Title:       issue.Title,
		Branch:      slug.Branch(issue.Title, id),
		Description: issue.Body,
		Phase:       "unassigned",
		IssueNumber: &number,
		IssueURL:    issue.HTMLURL,
	}
	createErr := s.DB.Create(&ticket).Error
	if createErr != nil && isDuplicateKey(createErr) {
		// A concurrent pass already created this ticket; its row is the one
		// that counts.
		return nil
	}
	if createErr != nil {
		return fmt.Errorf("create ticket for issue #%d: %w", issue.Number, createErr)
	}
	return nil
}

// recordRepoError stores a poll failure on the repo row so the dashboard can
// show it. A failure for one repo never stops the others. The write is
// best-effort: if it fails too, the original GitHub error (already returned
// to the caller) is what matters, so this only logs rather than compounding
// the error return.
func (s *Syncer) recordRepoError(repo *db.GitHubRepo, err error) {
	now := time.Now()
	if uerr := s.DB.Model(repo).Updates(map[string]any{
		"last_polled_at": now,
		"last_error":     err.Error(),
	}).Error; uerr != nil {
		log.Printf("ghsync: repo %s: failed to record poll error: %v", repo.RepoRemote, uerr)
	}
}
