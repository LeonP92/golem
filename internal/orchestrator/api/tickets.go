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
	"github.com/leonp92/golem/internal/orchestrator/rbac"
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
	TicketID string `json:"ticket_id"`
	Branch   string `json:"branch"`
	// BaseBranch is what a conflict is resolved against. The shem fetches
	// it into the agent's repo so `git merge origin/<base>` works without
	// handing the agent a credential, and it cannot be read from the
	// worktree's own config because the agent owns and can rewrite that.
	BaseBranch      string        `json:"base_branch"`
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
//
// The non-empty test is not redundant with the equality (re-review finding
// F1). A database last written by a build in the window [f9e87e0, fef123b)
// — after intake_approved shipped, before the two hash columns did —
// migrates forward with AutoMigrate adding both columns at their zero
// value, so a ticket a human approved under that build arrives as
//
//	intake_approved = 1, approved_body_hash = '', body_hash = ''
//
// and the equality alone evaluates to TRUE, because the empty string equals
// itself. That row was claimable with no hash binding at all — and that
// build's applyIssue had no re-gate either, so its stored description may be
// text edited after the approval and read by nobody. Emptiness is not
// approval. Requiring a non-empty approved_body_hash costs one comparison
// and makes the whole class unreachable regardless of migration history.
// issue_number IS NULL still short-circuits ahead of it, so web-form
// tickets — which carry two empty hashes for their whole life — are
// unaffected. admin.BackfillBodyHash repairs such rows;
// TestEmptyHashesAreNotAnApproval pins all four predicates.
func (h *Handlers) ClaimTicket(ticketID string, shemID uint) (*ClaimResponse, error) {
	result := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND phase = 'unassigned' AND "+
			"(issue_number IS NULL OR (intake_approved AND approved_body_hash <> '' AND approved_body_hash = body_hash))", ticketID).
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
		BaseBranch:      ticket.BaseBranch,
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
	mux.Handle("POST /api/tickets",
		auth.RequireSession(h.DB)(auth.RequireCSRF(rbac.Require(rbac.PermTicketCreate)(http.HandlerFunc(h.createTicket)))))
	mux.Handle("GET /api/tickets",
		auth.RequireSession(h.DB)(rbac.Require(rbac.PermTicketView)(http.HandlerFunc(h.listTickets))))
	mux.Handle("GET /api/tickets/available", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.availableTickets)))
	mux.Handle("GET /api/tickets/resumable", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.resumableTickets)))
	mux.Handle("GET /api/tickets/assigned", auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.assignedTickets)))
	mux.Handle("GET /api/tickets/{id}",
		auth.RequireSession(h.DB)(rbac.Require(rbac.PermTicketView)(http.HandlerFunc(h.getTicket))))
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

// resumableTickets returns tickets assigned to this shem that are in an
// active execution phase — i.e. were mid-run when the shem died.
//
// A checkpoint is NOT required. It used to be, and that left a hole: a shem
// that restarted during brainstorm — before the first checkpoint is written —
// owned a ticket it would never resume, in a phase that is not claimable
// either, so nothing could ever pick it up again. The shem's own startup log
// reported it as "waiting (no action needed)" while it waited forever.
//
// The checkpoint's job is to say which phases are already DONE so a resume can
// skip them. Its absence means only "start this phase over", which is what
// RunTicket already does when ClaimResponse.CheckpointPhase is nil.
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
		"assigned_shem = ? AND phase NOT IN ? AND "+
			"(issue_number IS NULL OR (intake_approved AND approved_body_hash <> '' AND approved_body_hash = body_hash))",
		shem.ID,
		// needs-attention is excluded for the reason the phase exists: it
		// means a run failed and a human has to look. Resuming it
		// automatically re-ran the failed work on every shem restart — a
		// ticket whose agent stopped for a reason the restart cannot change
		// (an unauthenticated nested `claude`, a question only a person can
		// answer) burned a full implementation pass per restart, forever, and
		// appended another round of near-identical entries each time.
		//
		// The way out of needs-attention is a human: Re-queue, which clears
		// the checkpoint and starts the ticket over, or Close.
		// "stopped" is here for a different reason from the rest: those are
		// phases where there is nothing to resume, this is one where a
		// human has said not to. A stop that a shem restart undoes is not
		// a stop.
		[]string{"unassigned", "ready-for-review", "revising", "closed", "needs-attention", "stopped"},
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
			BaseBranch:      t.BaseBranch,
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
		"(issue_number IS NULL OR (intake_approved AND approved_body_hash <> '' AND approved_body_hash = body_hash))")
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
			"(issue_number IS NULL OR (intake_approved AND approved_body_hash <> '' AND approved_body_hash = body_hash))",
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
		BaseBranch:      ticket.BaseBranch,
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
			// phase <> 'stopped' is what makes a stop hold. Cancelling
			// the agent is asynchronous, so when a human stops a ticket a
			// phase report is very likely already in flight; without this
			// clause that report wrote straight over the stop and answered
			// 204, leaving the ticket assigned, idle and resumable — the
			// opposite of the invariant actionStop documents.
			Where("id = ? AND assigned_shem = ? AND phase <> ?", id, shem.ID, PhaseStopped).
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

// writeTicketOwnership reports whether the ticket is assigned to the shem
// this request authenticated as, writing the refusal response itself and
// returning false when it is not. Callers return immediately on false.
//
// Finding S8: postLog, postLogDocument, createHumanInput, listHumanInputs
// and resolveHumanInput checked that the caller was *a* shem and never that
// it was *this ticket's* shem, so any registered shem could write into any
// ticket's log — which is the dashboard's markdown sink — and overwrite any
// ticket's SPEC or PLAN. That matters more on this branch than it did
// before it, because a GitHub-sourced ticket's text is now written by
// strangers and drives an agent that holds the shem's API key.
//
// 409 and this message match the convention the newer shem writes already
// use (branch-pushed, PATCH phase, revise-claim): a request that is
// authenticated but aimed at someone else's ticket is a conflict, not an
// authentication failure, and the response says nothing about whether the
// ticket exists. The shem client only ever calls these for a ticket it has
// claimed, so no legitimate caller sees this.
func (h *Handlers) writeTicketOwnership(w http.ResponseWriter, r *http.Request, ticketID string) bool {
	shem := auth.ShemFromRequest(r)
	if shem == nil {
		http.Error(w, errNotOwner.Error(), http.StatusConflict)
		return false
	}
	var count int64
	if err := h.DB.Model(&db.Ticket{}).
		Where("id = ? AND assigned_shem = ?", ticketID, shem.ID).
		Count(&count).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	if count == 0 {
		http.Error(w, errNotOwner.Error(), http.StatusConflict)
		return false
	}
	return true
}

// enqueueGitHubPhase queues the label and milestone-comment writes for a phase
// transition on a GitHub-linked ticket. Unlinked tickets (nil IssueNumber) are
// a no-op — every ticket created through the web UI is unlinked, and those
// flows must see no outbox activity at all.
func enqueueGitHubPhase(tx *gorm.DB, ticket db.Ticket, phase, baseURL string) error {
	if ticket.IssueNumber == nil {
		return nil
	}
	// ticket must be the row as re-read inside tx after this transaction's own
	// phase write — every caller does that — or the label's transition ordinal
	// comes from a snapshot another writer has already superseded.
	if err := ghsync.EnqueuePhaseLabel(tx, ticket, phase); err != nil {
		return err
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
