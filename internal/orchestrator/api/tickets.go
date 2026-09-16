package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"github.com/leonp92/golem/internal/slug"
	"gorm.io/gorm"
)

// ticketResponse wraps a db.Ticket with a resolved creator username for
// JSON responses, so clients don't need a second lookup to render
// "created by X".
type ticketResponse struct {
	db.Ticket
	CreatedBy string `json:"created_by"`
}

func toTicketResponse(t db.Ticket, names map[uint]string) ticketResponse {
	created := ""
	if t.CreatedByUserID != nil {
		created = names[*t.CreatedByUserID]
	}
	return ticketResponse{Ticket: t, CreatedBy: created}
}

// ClaimResponse is returned by a successful ticket claim.
type ClaimResponse struct {
	TicketID        string        `json:"ticket_id"`
	Branch          string        `json:"branch"`
	Title           string        `json:"title"`
	RepoRemote      string        `json:"repo_remote"`
	Description     string        `json:"description"`
	CheckpointPhase *string       `json:"checkpoint_phase"`
	CheckpointSHA   *string       `json:"checkpoint_sha"`
	LogEntries      []db.LogEntry `json:"log_entries"`
}

// ClaimTicket atomically claims a ticket for shemID. Exactly one concurrent
// caller wins; all others receive a non-nil error.
//
// The (issue_number IS NULL OR (intake_approved AND approved_body_hash =
// body_hash)) half of the predicate (spec Amendment 1) is provenance, not
// phase: an externally-sourced ticket (non-nil issue_number) must have been
// explicitly released by actionStart, regardless of what phase gymnastics
// (close, needs-attention, requeue, ...) it has been through since. A
// web-form ticket has a nil issue_number and is unaffected.
//
// Checking approved_body_hash = body_hash, not just intake_approved, closes
// a fix-round-4 gap (round 5): a ticket claimed while approved deliberately
// keeps intake_approved=true and its now-stale approved_body_hash if the
// issue is edited afterward (the running shem is not yanked — see
// ghsync.applyIssue), but body_hash keeps tracking the live issue. Without
// this half of the predicate, that ticket became claimable again — with the
// attacker's edited text — the moment it returned to the pool via requeue
// or the heartbeat reaper, neither of which re-checks the hash. Requiring
// equality here means approval means "a human approved THIS text", checked
// at the one place it matters, regardless of how many paths return a
// ticket to the pool now or later.
func (h *Handlers) ClaimTicket(ticketID string, shemID uint) (*ClaimResponse, error) {
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND phase = 'unassigned' AND "+
			"(issue_number IS NULL OR (intake_approved AND approved_body_hash = body_hash))", ticketID).
		Updates(map[string]any{"phase": "claimed", "assigned_shem": shemID})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("ticket not available")
	}
	var ticket db.Ticket
	h.DB.First(&ticket, "id = ?", ticketID)
	var entries []db.LogEntry
	h.DB.Where("ticket_id = ?", ticketID).Order("sequence_num asc").Find(&entries)
	if entries == nil {
		entries = []db.LogEntry{}
	}
	return &ClaimResponse{
		TicketID:        ticketID,
		Branch:          ticket.Branch,
		Title:           ticket.Title,
		RepoRemote:      ticket.RepoRemote,
		Description:     ticket.Description,
		CheckpointPhase: ticket.CheckpointPhase,
		CheckpointSHA:   ticket.CheckpointSHA,
		LogEntries:      entries,
	}, nil
}

// RegisterTicketRoutes adds ticket-related routes to mux.
func (h *Handlers) RegisterTicketRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/tickets", auth.RequireSession(h.DB)(http.HandlerFunc(h.createTicket)))
	mux.Handle("GET /api/tickets", auth.RequireSession(h.DB)(http.HandlerFunc(h.listTickets)))
	mux.Handle("GET /api/tickets/available", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.availableTickets)))
	mux.Handle("GET /api/tickets/resumable", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.resumableTickets)))
	mux.Handle("GET /api/tickets/assigned", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.assignedTickets)))
	mux.Handle("GET /api/tickets/{id}", auth.RequireSession(h.DB)(http.HandlerFunc(h.getTicket)))
	mux.Handle("POST /api/tickets/{id}/claim", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.claimTicket)))
	mux.Handle("POST /api/tickets/{id}/revise-claim", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.reviseClaim)))
	mux.Handle("PATCH /api/tickets/{id}/phase", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.updatePhase)))
	mux.Handle("PATCH /api/tickets/{id}/checkpoint", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.updateCheckpoint)))
}

// assignedTickets returns a summary of all tickets currently assigned to this shem.
func (h *Handlers) assignedTickets(w http.ResponseWriter, r *http.Request) {
	shem := auth.ShemFromRequest(r)
	var tickets []db.Ticket
	h.DB.Where("assigned_shem = ? AND phase NOT IN ?", shem.ID, []string{"unassigned", "closed"}).
		Select("id, phase, repo_remote").Find(&tickets)
	type summary struct {
		TicketID   string `json:"ticket_id"`
		Phase      string `json:"phase"`
		RepoRemote string `json:"repo_remote"`
	}
	out := make([]summary, len(tickets))
	for i, t := range tickets {
		out[i] = summary{TicketID: t.ID, Phase: t.Phase, RepoRemote: t.RepoRemote}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out) //nolint:errcheck
}

// resumableTickets returns tickets assigned to this shem that have a checkpoint
// and are in an active execution phase — i.e. were mid-run when the shem died.
//
// The approval clause matches ClaimTicket's (fix round 5: approved_body_hash
// = body_hash, not just intake_approved — see ClaimTicket's doc comment for
// why). It is not purely defence in depth here: if the issue was edited
// after approval while this ticket was claimed, applyIssue deliberately does
// not yank it (the shem may be mid-run), but Description is still refreshed
// to the edited text. Without this clause, a shem restarting and resuming
// via this endpoint would receive that edited text to continue working
// from — the same exposure this task exists to close, reached through
// resume instead of claim. Excluding it here means a restarted shem simply
// does not resume it; the ticket stays assigned until a human re-approves
// and something (currently nothing automatic) requeues it.
func (h *Handlers) resumableTickets(w http.ResponseWriter, r *http.Request) {
	shem := auth.ShemFromRequest(r)
	var tickets []db.Ticket
	h.DB.Where(
		"assigned_shem = ? AND checkpoint_phase IS NOT NULL AND phase NOT IN ? AND "+
			"(issue_number IS NULL OR (intake_approved AND approved_body_hash = body_hash))",
		shem.ID,
		[]string{"unassigned", "ready-for-review", "revising", "closed"},
	).Find(&tickets)

	claims := make([]ClaimResponse, 0, len(tickets))
	for _, t := range tickets {
		var entries []db.LogEntry
		h.DB.Where("ticket_id = ?", t.ID).Order("sequence_num asc").Find(&entries)
		if entries == nil {
			entries = []db.LogEntry{}
		}
		claims = append(claims, ClaimResponse{
			TicketID:        t.ID,
			Branch:          t.Branch,
			Title:           t.Title,
			RepoRemote:      t.RepoRemote,
			Description:     t.Description,
			CheckpointPhase: t.CheckpointPhase,
			CheckpointSHA:   t.CheckpointSHA,
			LogEntries:      entries,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(claims) //nolint:errcheck
}

func (h *Handlers) createTicket(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RepoRemote  string `json:"repo_remote"`
		Branch      string `json:"branch"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.RepoRemote == "" || body.Branch == "" || body.Title == "" || body.Description == "" {
		http.Error(w, "repo_remote, branch, title, and description are required", http.StatusBadRequest)
		return
	}
	user := auth.SessionUser(r)
	id := uuid.NewString()
	ticket := db.Ticket{
		ID:          id,
		RepoRemote:  urlnorm.Normalize(body.RepoRemote),
		BaseBranch:  body.Branch,
		Title:       body.Title,
		Branch:      slug.Branch(body.Title, id),
		Description: body.Description,
		Phase:       "unassigned",
	}
	if user != nil {
		ticket.CreatedByUserID = &user.ID
	}
	if err := h.DB.Create(&ticket).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	names := db.CreatorNames(h.DB, []db.Ticket{ticket})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toTicketResponse(ticket, names)) //nolint:errcheck
}

func (h *Handlers) listTickets(w http.ResponseWriter, r *http.Request) {
	var tickets []db.Ticket
	h.DB.Order("created_at desc").Find(&tickets)
	names := db.CreatorNames(h.DB, tickets)
	resp := make([]ticketResponse, len(tickets))
	for i, t := range tickets {
		resp[i] = toTicketResponse(t, names)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (h *Handlers) getTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	names := db.CreatorNames(h.DB, []db.Ticket{ticket})
	var entries []db.LogEntry
	h.DB.Where("ticket_id = ?", id).Order("sequence_num asc").Find(&entries)
	if entries == nil {
		entries = []db.LogEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"ticket":      toTicketResponse(ticket, names),
		"log_entries": entries,
	})
}

// availableTickets lists claimable tickets. See ClaimTicket's doc comment for
// why intake_approved, not phase alone, gates externally-sourced tickets.
func (h *Handlers) availableTickets(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	query := h.DB.Where("phase = 'unassigned' AND " +
		"(issue_number IS NULL OR (intake_approved AND approved_body_hash = body_hash))")
	if repo != "" {
		query = query.Where("repo_remote = ?", urlnorm.Normalize(repo))
	}
	var tickets []db.Ticket
	query.Order("created_at asc").Find(&tickets)
	if tickets == nil {
		tickets = []db.Ticket{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tickets) //nolint:errcheck
}

func (h *Handlers) claimTicket(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	resp, err := h.ClaimTicket(id, shem.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	h.Hub.Broadcast(resp.RepoRemote, ws.WSMessage{
		Type:     "ticket_claimed",
		TicketID: strPtr(resp.TicketID),
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

// ReviseClaim resumes a ticket already assigned to shemID that is in the
// revising phase. Unlike ClaimTicket it does not change phase or
// assigned_shem — the ticket is already owned by this shem.
//
// The approval clause matches resumableTickets' posture (Task 20): a ticket
// can sit in ready-for-review for hours or days awaiting a human, visible on
// GitHub the whole time via the golem:ready-for-review label mirror. If the
// issue is edited during that window and the routine human
// "request-changes" action moves the ticket to revising, ghsync.applyIssue
// refreshes Description/BodyHash but — because the ticket is still
// assigned — deliberately does not yank phase or IntakeApproved back to
// pending-approval (the running shem may be mid-run). Without this clause,
// revise-claim would serve that edited, never-reviewed text straight into
// buildRevisePrompt with no race required. Requiring
// approved_body_hash = body_hash here means approval means "a human
// approved THIS text", checked at the point revise-claim actually hands the
// description to a shem; a mismatch simply refuses the resume and leaves a
// human to re-approve, exactly as resumableTickets does.
func (h *Handlers) ReviseClaim(ticketID string, shemID uint) (*ClaimResponse, error) {
	var ticket db.Ticket
	result := h.DB.
		Where("id = ? AND phase = 'revising' AND assigned_shem = ? AND "+
			"(issue_number IS NULL OR (intake_approved AND approved_body_hash = body_hash))",
			ticketID, shemID).
		First(&ticket)
	if result.Error != nil {
		return nil, fmt.Errorf("ticket not available for revision")
	}
	var entries []db.LogEntry
	h.DB.Where("ticket_id = ?", ticketID).Order("sequence_num asc").Find(&entries)
	if entries == nil {
		entries = []db.LogEntry{}
	}
	revisingPhase := "revising"
	return &ClaimResponse{
		TicketID:        ticketID,
		Branch:          ticket.Branch,
		RepoRemote:      ticket.RepoRemote,
		Description:     ticket.Description,
		CheckpointPhase: &revisingPhase,
		CheckpointSHA:   ticket.CheckpointSHA,
		LogEntries:      entries,
	}, nil
}

func (h *Handlers) reviseClaim(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	resp, err := h.ReviseClaim(id, shem.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (h *Handlers) updatePhase(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	var body struct {
		Phase string `json:"phase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Phase == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	validPhases := map[string]bool{
		"brainstorm": true, "plan": true, "implement": true,
		"review": true, "ready-for-review": true, "needs-attention": true,
		"in-progress": true,
	}
	if !validPhases[body.Phase] {
		http.Error(w, "invalid phase", http.StatusBadRequest)
		return
	}

	// The phase update and its GitHub follow-ups commit together, so a phase
	// can never be recorded without its writes queued.
	//
	// The ticket handed to enqueueGitHubPhase is reloaded via tx.First AFTER
	// the Updates call below succeeds, not fetched before this transaction
	// opens (fix round 1: a prior version read the ticket via a plain,
	// pre-transaction h.DB.First). That plain read takes no lock, so a
	// fully separate, concurrent write — e.g. POST .../branch-pushed
	// committing between the read and this transaction's own write — could
	// land in the gap, and enqueueGitHubPhase would then decide the PR
	// precondition (BranchPushed) from a snapshot that was already stale by
	// the time it ran. That inverts the failure the outbox's idempotency
	// key guards against: instead of two writers racing to a duplicate PR
	// (which the unique key already prevents), BOTH writers would correctly
	// see their own precondition as unmet and both decline, leaving the row
	// with phase = ready-for-review AND branch_pushed = true but zero pr
	// rows — and nothing recovers it, since reconcile.go never calls
	// CreatePullRequest. Reloading via tx.First after this transaction's own
	// write closes the gap: no backend lets a second writer commit against
	// this row while this transaction holds it, so the reload is guaranteed
	// to see every write already committed against this row, plus this
	// transaction's own just-applied phase change — never anything staler.
	// This applies to every field enqueueGitHubPhase/enqueuePRIfReady reads
	// (IssueNumber, BranchPushed, Phase, BaseBranch, Branch, Title), not
	// just BranchPushed — none of them can come from a snapshot older than
	// this transaction's own write once sourced this way.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND assigned_shem = ?", id, shem.ID).
			Updates(map[string]any{"phase": body.Phase})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotOwner
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		return enqueueGitHubPhase(tx, fresh, body.Phase, h.BaseURL)
	})
	if errors.Is(txErr, errNotOwner) {
		http.Error(w, "ticket not owned by this shem", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// errNotOwner signals that the ticket is not assigned to the calling shem.
var errNotOwner = errors.New("ticket not owned by this shem")

// enqueueGitHubPhase queues the label and milestone-comment writes for a phase
// transition on a GitHub-linked ticket. Unlinked tickets (nil IssueNumber) are
// a no-op — every ticket created through the web UI is unlinked, and those
// flows must see no outbox activity at all.
func enqueueGitHubPhase(tx *gorm.DB, ticket db.Ticket, phase, baseURL string) error {
	if ticket.IssueNumber == nil {
		return nil
	}
	labelPayload, err := json.Marshal(ghsync.LabelPayload{Phase: phase})
	if err != nil {
		return fmt.Errorf("marshal label payload: %w", err)
	}
	if err := ghsync.Enqueue(tx, db.GitHubOutbox{
		TicketID:       ticket.ID,
		Kind:           ghsync.KindLabel,
		Payload:        string(labelPayload),
		IdempotencyKey: ghsync.LabelKey(ticket.ID, phase),
	}); err != nil {
		return fmt.Errorf("enqueue label: %w", err)
	}

	// The PR check runs for every phase transition, not just ones with a
	// milestone comment, so it must not sit behind the !ok early return
	// below. phase is set on a copy explicitly and unconditionally, rather
	// than trusting ticket.Phase to already match it: updatePhase's caller
	// reloads ticket after its own write (so ticket.Phase already agrees),
	// but actionStart's does not (it passes "unassigned", a phase that can
	// never satisfy enqueuePRIfReady's ready-for-review check regardless),
	// and enqueueGitHubPhase must give both callers the same guarantee
	// rather than depending on which one already agrees.
	updated := ticket
	updated.Phase = phase
	if err := enqueuePRIfReady(tx, updated); err != nil {
		return err
	}

	milestone, body, ok := ghsync.MilestoneComment(phase, ticket.ID, baseURL)
	if !ok {
		return nil
	}
	commentPayload, err := json.Marshal(ghsync.CommentPayload{Body: body})
	if err != nil {
		return fmt.Errorf("marshal comment payload: %w", err)
	}
	if err := ghsync.Enqueue(tx, db.GitHubOutbox{
		TicketID:       ticket.ID,
		Kind:           ghsync.KindComment,
		Payload:        string(commentPayload),
		IdempotencyKey: ghsync.CommentKey(ticket.ID, milestone),
	}); err != nil {
		return fmt.Errorf("enqueue comment: %w", err)
	}
	return nil
}

func (h *Handlers) updateCheckpoint(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	shem := auth.ShemFromRequest(r)
	var body struct {
		Phase string `json:"checkpoint_phase"`
		SHA   string `json:"checkpoint_sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND assigned_shem = ?", id, shem.ID).
		Updates(map[string]any{
			"checkpoint_phase": body.Phase,
			"checkpoint_sha":   body.SHA,
		})
	if result.RowsAffected == 0 {
		http.Error(w, "not found or not owner", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseIDFromPath extracts the {id} path value from a request using Go 1.22+
// pattern matching (PathValue).
func parseIDFromPath(r *http.Request) (string, error) {
	raw := r.PathValue("id")
	if raw == "" {
		// Fallback: extract last path segment.
		parts := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
		raw = parts[len(parts)-1]
	}
	if raw == "" {
		return "", fmt.Errorf("missing id")
	}
	return raw, nil
}

// strPtr returns a pointer to the given string value.
func strPtr(v string) *string { return &v }
