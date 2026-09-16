package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/auth"
	"github.com/leonp92/golem/internal/orchestrator/db"
	"github.com/leonp92/golem/internal/orchestrator/ghsync"
	"github.com/leonp92/golem/internal/orchestrator/sse"
	ws "github.com/leonp92/golem/internal/orchestrator/ws"
	"gorm.io/gorm"
)

// RegisterHumanRoutes adds human-input management and ticket action routes to mux.
func (h *Handlers) RegisterHumanRoutes(mux *http.ServeMux) {
	// Shem-facing: CRUD for human inputs (API key auth, scoped to the
	// calling shem's own ticket — see writeTicketOwnership).
	mux.Handle("POST /api/tickets/{id}/human-inputs",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.createHumanInput)))
	mux.Handle("GET /api/tickets/{id}/human-inputs",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.listHumanInputs)))
	mux.Handle("PATCH /api/tickets/{id}/human-inputs/{inputID}",
		auth.RequireAPIKey(h.DB)(http.HandlerFunc(h.resolveHumanInput)))

	// Human-facing: single action dispatcher (session auth + CSRF).
	// RequireCSRF sits INSIDE RequireSession because it reads the expected
	// token out of the context RequireSession populates. Without it, markup
	// injected into the dashboard's markdown sink can make the operator's
	// own browser POST here — same origin, so SameSite=Lax does not help —
	// and "start" is the sole writer of intake_approved (finding S1).
	mux.Handle("POST /api/tickets/{id}/actions",
		auth.RequireSession(h.DB)(auth.RequireCSRF(http.HandlerFunc(h.ticketAction))))
}

// maxActionFormBytes caps the in-memory portion of a multipart action
// submission. The dashboard only ever sends urlencoded bodies; multipart is
// accepted for hand-written clients, and does not need to be large.
const maxActionFormBytes = 1 << 20

// ticketAction is the single dispatcher for all human-initiated ticket actions.
// Body: {"action": "approve"|"requeue"|"close"|"needs-attention"|"request-changes"|"answer"|"start",
//
//	"feedback": "...",   (requeue / request-changes)
//	"input_id": 123,     (answer)
//	"response": "..."}   (answer)
//
// Also accepts application/x-www-form-urlencoded for browser form submissions.
// Every parameter is read from the request BODY only; nothing is taken from
// the URL query string (see the r.PostForm comment below).
func (h *Handlers) ticketAction(w http.ResponseWriter, r *http.Request) {
	id, err := parseIDFromPath(r)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	var body struct {
		Action   string `json:"action"`
		Feedback string `json:"feedback"`
		InputID  uint   `json:"input_id"`
		Response string `json:"response"`
		// ReviewedBodyHash is the ticket's body_hash as it was when the
		// page carrying this control was rendered — i.e. the fingerprint
		// of the description the operator actually read. Only "start"
		// uses it; see actionStart.
		ReviewedBodyHash string `json:"reviewed_body_hash"`
	}

	ct := r.Header.Get("Content-Type")
	isURLEncoded := strings.HasPrefix(ct, "application/x-www-form-urlencoded")
	isMultipart := strings.HasPrefix(ct, "multipart/form-data")
	if isURLEncoded || isMultipart {
		// ParseForm alone does not read a multipart body into PostForm —
		// only ParseMultipartForm does — so dispatch on the content type
		// rather than relying on r.FormValue to do it lazily (it would also
		// re-introduce the query-string merge described below).
		var parseErr error
		if isMultipart {
			parseErr = r.ParseMultipartForm(maxActionFormBytes)
		} else {
			parseErr = r.ParseForm()
		}
		if parseErr != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		// r.PostForm, not r.FormValue: ParseForm merges the URL query into
		// r.Form, so r.FormValue("action") would accept an action supplied
		// entirely in the query string of an otherwise empty POST. That is
		// what lets an injected <form action="/api/tickets/X/actions?action=
		// start"> work with no form fields of its own — DOMPurify strips
		// name= from <input> (DOM-clobbering defence), so carrying the
		// parameters in the form's own URL is the attacker's whole trick
		// (finding S1). The dashboard's own controls put every parameter in
		// the body: htmx serializes hx-vals and <input name=...> into the
		// request body for a POST.
		body.Action = r.PostForm.Get("action")
		body.Feedback = r.PostForm.Get("feedback")
		body.Response = r.PostForm.Get("response")
		body.ReviewedBodyHash = r.PostForm.Get("reviewed_body_hash")
		if idStr := r.PostForm.Get("input_id"); idStr != "" {
			id64, _ := strconv.ParseUint(idStr, 10, 64)
			body.InputID = uint(id64)
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Action == "" {
			http.Error(w, "action is required", http.StatusBadRequest)
			return
		}
	}

	if body.Action == "" {
		http.Error(w, "action is required", http.StatusBadRequest)
		return
	}

	switch body.Action {
	case "approve":
		h.actionApprove(w, r, id)
	case "requeue":
		h.actionRequeue(w, r, id, body.Feedback)
	case "close":
		h.actionClose(w, r, id)
	case "needs-attention":
		h.actionNeedsAttention(w, r, id)
	case "request-changes":
		if body.Feedback == "" {
			http.Error(w, "feedback is required for request-changes", http.StatusBadRequest)
			return
		}
		h.actionRequestChanges(w, r, id, body.Feedback)
	case "answer":
		if body.InputID == 0 || body.Response == "" {
			http.Error(w, "input_id and response are required for answer", http.StatusBadRequest)
			return
		}
		h.actionAnswer(w, r, id, body.InputID, body.Response)
	case "start":
		// Validated here, alongside the dispatcher's other per-action
		// required parameters. An absent hash is not "no opinion" — it is
		// exactly the shape a page cached from before this check produces,
		// and the shape any caller wanting to skip the check would use, so
		// it is refused rather than waved through.
		if body.ReviewedBodyHash == "" {
			http.Error(w, "reviewed_body_hash is required for start; "+
				"reload the ticket page and read the description again", http.StatusBadRequest)
			return
		}
		h.actionStart(w, r, id, body.ReviewedBodyHash)
	default:
		http.Error(w, "unknown action: "+body.Action, http.StatusBadRequest)
	}
}

func (h *Handlers) actionApprove(w http.ResponseWriter, r *http.Request, id string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	nextPhase, ok := nextApprovalPhase(ticket.Phase)
	if !ok {
		http.Error(w, "ticket phase does not accept approval", http.StatusBadRequest)
		return
	}

	var hi db.HumanInput
	if err := h.DB.
		Where("ticket_id = ? AND kind = 'approval' AND resolved_at IS NULL", id).
		Order("created_at asc").
		First(&hi).Error; err != nil {
		http.Error(w, "no pending approval found", http.StatusNotFound)
		return
	}

	now := time.Now()
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND resolved_at IS NULL", hi.ID).
		Updates(map[string]any{"response": "approved", "resolved_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// The phase change and its GitHub label write commit together, like
	// every other transition (finding I4, round 1c). Before this, approve
	// was a bare Update that queued nothing, so a linked ticket advancing
	// brainstorm -> plan -> implement kept whatever golem:* label it had
	// until some later reconcile pass noticed — and reconcile is skipped on
	// a 304, which is what a quiet repository answers.
	if err := h.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&db.Ticket{}).Where("id = ?", id).
			Update("phase", nextPhase).Error; err != nil {
			return err
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		return enqueueGitHubPhase(tx, fresh, nextPhase, h.BaseURL)
	}); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(id, "ANSWER", "human", "", "Approved: "+hi.Prompt); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if ticket.AssignedShem != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type:     "human_input_resolved",
			TicketID: strPtr(id),
			InputID:  &hi.ID,
			Response: "approved",
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionRequeue(w http.ResponseWriter, r *http.Request, id string, feedback string) {
	// Read before clearing so we can notify the currently-assigned shem.
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	// pending-approval is deliberately excluded alongside unassigned/closed:
	// re-queue is a generic "unstick it" action for a ticket a shem has
	// already touched, not a substitute for the human review that "start"
	// performs on a never-run, externally-sourced ticket (spec Amendment 1).
	// The phase change and its GitHub label write commit together, the same
	// way updatePhase's do. Before fix round 1b this was a bare Update that
	// queued nothing, so a linked ticket dropped back to unassigned kept
	// whatever golem:* label it was last given until some later reconcile
	// pass happened to notice — and on a quiet repo the poll answers 304 and
	// reconcile does not run at all (finding I4). The WHERE clause is
	// unchanged.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase NOT IN ('unassigned', 'pending-approval', 'closed')", id).
			Updates(map[string]any{
				"phase":            "unassigned",
				"assigned_shem":    nil,
				"checkpoint_phase": nil,
				"checkpoint_sha":   nil,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotRequeueable
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		return enqueueGitHubPhase(tx, fresh, "unassigned", h.BaseURL)
	})
	if errors.Is(txErr, errNotRequeueable) {
		http.Error(w, "ticket is unassigned, closed, or awaiting intake approval", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.DB.Where("ticket_id = ? AND resolved_at IS NULL", id).Delete(&db.HumanInput{})

	if feedback != "" {
		if err := h.appendLog(id, "HUMAN_FEEDBACK", "human", "", feedback); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Tell the currently-running shem to stop (context cancel, no worktree cleanup).
	if ticket.AssignedShem != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type:     "ticket_requeued",
			TicketID: strPtr(id),
			Repo:     ticket.RepoRemote,
		})
	}

	// Broadcast availability to all shems watching this repo.
	h.Hub.Broadcast(ticket.RepoRemote, ws.WSMessage{
		Type:     "ticket_available",
		TicketID: strPtr(id),
		Repo:     ticket.RepoRemote,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionClose(w http.ResponseWriter, r *http.Request, id string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	// The close transition and its GitHub close-issue write commit together,
	// so a close can never be recorded without its follow-up queued.
	//
	// The issue-linkage decision below is made from a row re-read inside
	// this transaction, not from the pre-transaction read above (fix round
	// 1b, minor m11 — the same defect class as I5). Ingest does not
	// currently link an already-existing ticket to an issue, so no writer
	// moves issue_number from NULL to non-NULL today, but deciding a
	// follow-up write from a snapshot this transaction has already
	// superseded is the pattern that produced I5, and it is applied
	// family-wide here rather than left as the one remaining instance.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase != 'closed'", id).
			Update("phase", "closed")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errAlreadyClosed
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		if fresh.IssueNumber == nil {
			return nil
		}
		if err := ghsync.Enqueue(tx, db.GitHubOutbox{
			TicketID:       id,
			Kind:           ghsync.KindClose,
			Payload:        "{}",
			IdempotencyKey: ghsync.CloseKey(id),
		}); err != nil {
			return fmt.Errorf("enqueue close: %w", err)
		}
		return nil
	})
	if errors.Is(txErr, errAlreadyClosed) {
		http.Error(w, "ticket already closed", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.DB.Where("ticket_id = ? AND resolved_at IS NULL", id).Delete(&db.HumanInput{})

	if ticket.AssignedShem != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type:     "ticket_closed",
			TicketID: &id,
			Repo:     ticket.RepoRemote,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// errAlreadyClosed signals that the ticket was already in the closed phase
// when a close action was attempted.
var errAlreadyClosed = errors.New("ticket already closed")

// errNotRequeueable signals that the ticket was unassigned, closed, or still
// awaiting intake approval when a requeue action was attempted.
var errNotRequeueable = errors.New("ticket is not requeueable")

// errNotFlaggable signals that the ticket did not exist, or was still
// awaiting intake approval, when a needs-attention action was attempted.
var errNotFlaggable = errors.New("ticket is not flaggable")

// errNotInReview signals that the ticket left ready-for-review before a
// request-changes submitted from the review screen could move it to revising.
var errNotInReview = errors.New("ticket is not in ready-for-review")

// actionStart releases an externally-ingested ticket for execution, moving it
// to unassigned so a shem can claim it. This is the human checkpoint required
// before any agent prompt is built from an issue body written by a stranger
// (spec Amendment 1).
//
// actionStart is the ONLY writer of intake_approved and approved_body_hash.
// Claimability is enforced against those columns, not against phase (see
// ClaimTicket and availableTickets in tickets.go): phase is a transient
// UX/GitHub-label concern that other handlers legitimately move a ticket
// through (close, needs-attention, requeue, ...), so guarding phase
// transitions one at a time cannot close this off for good — a ticket that
// legitimately leaves pending-approval by any path still carries whatever
// intake_approved was before that transition.
//
// The guard below is deliberately provenance-based (issue_number set,
// intake_approved false, not closed, not claimed) rather than phase-based
// (fix round 4). A GitHub-sourced ticket that leaves pending-approval by any
// route other than start (needs-attention, requeue, ...) still has
// intake_approved=false and is therefore still unclaimable, but under a
// phase-only guard it had NO in-product recovery: one misclick of the
// dashboard's own Close or Re-queue button bricked it permanently, since
// re-ingesting the same issue is blocked by the unique
// (repo_remote, issue_number) index. Matching the guard to the actual
// invariant it establishes — rather than to one specific phase that
// invariant usually starts from — makes that recoverable. A closed ticket
// is deliberately excluded: reopening one is a separate action from
// starting it. assigned_shem IS NULL is also required (fix round 5): the
// phase check alone let a claimed-but-unapproved ticket (a state that
// should not arise, but round 4's guard did not defend against it) get
// double-claimed — see the comment on that clause below.
func (h *Handlers) actionStart(w http.ResponseWriter, r *http.Request, id, reviewedBodyHash string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	// The release transition and its GitHub label write commit together, so a
	// release can never be recorded without its follow-up queued.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		// assigned_shem IS NULL closes a fix-round-4 double-claim (round 5):
		// the provenance-based guard below checks intake_approved, not
		// claim status, so without this a ticket that is claimed and
		// mid-execution but somehow unapproved was startable — 204, phase
		// moved to unassigned with assigned_shem still set, and a second
		// shem could then claim the same ticket and branch. A claimed
		// ticket has nothing for "start" to do: it was already released
		// once (that is how it got claimed), and un-claiming it is
		// requeue's job, not start's — requeue also pushes ticket_requeued
		// to the running shem, which start still does not do, so start
		// must not perform requeue's job under a different name.
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND issue_number IS NOT NULL AND intake_approved = false "+
				"AND phase != 'closed' AND assigned_shem IS NULL", id).
			Updates(map[string]any{
				"phase":           "unassigned",
				"intake_approved": true,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotStartable
		}
		// Reload inside the transaction, after its own write, and hash THAT
		// Description (fix round 1b, finding I5). The ticket read above was
		// taken with a plain, pre-transaction h.DB.First, which takes no
		// lock: a ghsync.applyIssue commit landing in the gap meant the
		// approval was stamped with approved_body_hash = H(old) while
		// body_hash had already moved to H(new). The claim predicates
		// require the two to be equal, so that failed closed — but with no
		// in-product recovery: the ticket showed as released, a second
		// start 409ed on intake_approved, and requeue excludes unassigned.
		// Once this transaction has written the row no other writer can
		// commit against it until we commit, so this read sees every
		// committed write plus our own and never anything staler. Same
		// shape as updatePhase (commit 5aaaa5a) and branchPushed.
		//
		// This deliberately does NOT write body_hash, and must never be
		// "tidied up" to do so. body_hash is ghsync's alone — it is the
		// record of what the live issue currently says. Stamping both
		// columns from one read here would make the ticket claimable by
		// construction no matter what the issue had been edited to, which
		// is precisely the gate this branch exists to build. Writing only
		// approved_body_hash keeps the invariant one-directional: approval
		// can only ever certify text that ingest has already hashed.
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		// approvedHash binds this approval to the exact description text
		// that is on the ticket now (spec Amendment 1 fix round 4):
		// ghsync.applyIssue re-gates (clears intake_approved and this hash,
		// and returns the ticket to pending-approval) if a later poll finds
		// the live issue body no longer hashes to what was approved here,
		// for any ticket that has not yet been claimed.
		approvedHash := ghsync.HashBody(fresh.Description)

		// ...and reviewedBodyHash binds it to the text the operator was
		// actually SHOWN (fix round 1c). Round 1b closed the window between
		// this handler's own read and its transaction, but the window that
		// matters is longer and cannot be seen from inside this request at
		// all: the time between the page being rendered and the operator
		// clicking Approve, which is however long a human spends reading.
		// An ingest pass landing in there leaves this transaction reading
		// the edited text, and hashing it would certify text nobody
		// reviewed — fail-open, on the one property this gate exists to
		// provide. The page therefore renders the hash it displayed the
		// description at, the control submits it, and it has to still hold
		// here.
		//
		// Both comparisons are required. reviewedBodyHash == fresh.BodyHash
		// is the actual check. approvedHash == fresh.BodyHash additionally
		// refuses to approve a row whose two ghsync-written columns
		// disagree — a state nothing produces today, but one where a silent
		// 204 would strand the ticket unclaimable with no recovery (the S5
		// shape). Failing loudly here means an approval that returns 204 has
		// always left the ticket genuinely claimable.
		//
		// This check is deliberately after the UPDATE above rather than
		// before it: the row is only held against concurrent writers once
		// this transaction has written it, so a comparison made before that
		// could be overtaken by the very ingest commit it is meant to catch.
		// Refusing rolls the whole transaction back, including the phase and
		// intake_approved writes and any outbox row.
		if reviewedBodyHash != fresh.BodyHash || approvedHash != fresh.BodyHash {
			return errStaleReview
		}

		if err := tx.Model(&db.Ticket{}).Where("id = ?", id).
			Update("approved_body_hash", approvedHash).Error; err != nil {
			return fmt.Errorf("record approved body hash for ticket %s: %w", id, err)
		}
		fresh.ApprovedBodyHash = approvedHash
		return enqueueGitHubPhase(tx, fresh, "unassigned", h.BaseURL)
	})
	if errors.Is(txErr, errStaleReview) {
		http.Error(w, "the GitHub issue changed since this page was loaded; "+
			"reload the ticket and read the new description before approving it", http.StatusConflict)
		return
	}
	if errors.Is(txErr, errNotStartable) {
		http.Error(w, "ticket is already approved, already claimed, not linked to a GitHub issue, or closed", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(id, "STATUS", "human", "", "Approved and released for execution"); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Broadcast availability to all shems watching this repo, same as requeue.
	h.Hub.Broadcast(ticket.RepoRemote, ws.WSMessage{
		Type:     "ticket_available",
		TicketID: strPtr(id),
		Repo:     ticket.RepoRemote,
	})
	w.WriteHeader(http.StatusNoContent)
}

// errNotStartable signals that a start action was attempted on a ticket that
// is already approved (intake_approved), already claimed (assigned_shem set),
// not linked to a GitHub issue, or closed.
var errNotStartable = errors.New("ticket is not startable")

// errStaleReview signals that the description the operator approved is no
// longer the description on the ticket — an ingest pass committed an edit
// between the page being rendered and the approval arriving. Recovery is a
// reload: nothing is written, so the operator re-reads the new text and
// approves that.
var errStaleReview = errors.New("ticket body changed since it was reviewed")

func (h *Handlers) actionNeedsAttention(w http.ResponseWriter, r *http.Request, id string) {
	// pending-approval is excluded for the same reason requeue excludes it:
	// flagging a never-run, externally-sourced ticket moves it into a phase
	// that requeue *does* release, routing around the intake review
	// (spec Amendment 1).
	// As in actionRequeue, the phase change and its GitHub label write commit
	// together (finding I4). The WHERE clause is unchanged.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase != 'pending-approval'", id).
			Update("phase", "needs-attention")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotFlaggable
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", id).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", id, err)
		}
		return enqueueGitHubPhase(tx, fresh, "needs-attention", h.BaseURL)
	})
	if errors.Is(txErr, errNotFlaggable) {
		var count int64
		if err := h.DB.Model(&db.Ticket{}).Where("id = ?", id).Count(&count).Error; err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if count == 0 {
			http.Error(w, "ticket not found", http.StatusNotFound)
			return
		}
		http.Error(w, "ticket is awaiting intake approval", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionRequestChanges(w http.ResponseWriter, r *http.Request, id string, feedback string) {
	var ticket db.Ticket
	if err := h.DB.First(&ticket, "id = ?", id).Error; err != nil {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}
	if ticket.Phase == "ready-for-review" {
		h.requestChangesFromReview(w, r, ticket, feedback)
		return
	}

	var hi db.HumanInput
	if err := h.DB.
		Where("ticket_id = ? AND kind = 'approval' AND resolved_at IS NULL", id).
		Order("created_at asc").
		First(&hi).Error; err != nil {
		http.Error(w, "no pending approval found", http.StatusNotFound)
		return
	}
	now := time.Now()
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND resolved_at IS NULL", hi.ID).
		Updates(map[string]any{"response": "changes_requested", "resolved_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	fb := db.HumanInput{
		TicketID:  id,
		Kind:      "feedback",
		Prompt:    feedback,
		CreatedAt: now,
	}
	if err := h.DB.Create(&fb).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(id, "HUMAN_FEEDBACK", "human", "developer", feedback); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requestChangesFromReview handles request-changes submitted while a ticket
// is in ready-for-review: it moves the ticket to revising and wakes the
// assigned shem, instead of resolving a (nonexistent) pending approval.
func (h *Handlers) requestChangesFromReview(w http.ResponseWriter, r *http.Request, ticket db.Ticket, feedback string) {
	if ticket.AssignedShem == nil {
		http.Error(w, "ticket has no assigned shem; use requeue instead", http.StatusConflict)
		return
	}
	// As in actionApprove and actionRequeue, the phase change and its GitHub
	// label write commit together (finding I4, round 1c). The WHERE clause is
	// unchanged.
	txErr := h.DB.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&db.Ticket{}).
			Where("id = ? AND phase = 'ready-for-review'", ticket.ID).
			Update("phase", "revising")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errNotInReview
		}
		var fresh db.Ticket
		if err := tx.First(&fresh, "id = ?", ticket.ID).Error; err != nil {
			return fmt.Errorf("reload ticket %s: %w", ticket.ID, err)
		}
		return enqueueGitHubPhase(tx, fresh, "revising", h.BaseURL)
	})
	if errors.Is(txErr, errNotInReview) {
		http.Error(w, "ticket not in ready-for-review phase", http.StatusConflict)
		return
	}
	if txErr != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	fb := db.HumanInput{
		TicketID:  ticket.ID,
		Kind:      "feedback",
		Prompt:    feedback,
		CreatedAt: time.Now(),
	}
	if err := h.DB.Create(&fb).Error; err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := h.appendLog(ticket.ID, "HUMAN_FEEDBACK", "human", "developer", feedback); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
		Type:     "ticket_revise",
		TicketID: strPtr(ticket.ID),
		Repo:     ticket.RepoRemote,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) actionAnswer(w http.ResponseWriter, r *http.Request, id string, inputID uint, response string) {
	var hi db.HumanInput
	if err := h.DB.
		Where("id = ? AND ticket_id = ? AND resolved_at IS NULL", inputID, id).
		First(&hi).Error; err != nil {
		http.Error(w, "not found or already resolved", http.StatusNotFound)
		return
	}

	var originEntry db.LogEntry
	toRole := ""
	if h.DB.
		Where("ticket_id = ? AND message = ? AND to_role = 'human'", id, hi.Prompt).
		Order("sequence_num desc").
		First(&originEntry).Error == nil {
		toRole = originEntry.FromRole
	}

	now := time.Now()
	result := h.DB.Model(&db.HumanInput{}).
		Where("id = ? AND resolved_at IS NULL", hi.ID).
		Updates(map[string]any{"response": response, "resolved_at": now})
	if result.Error != nil || result.RowsAffected == 0 {
		http.Error(w, "already resolved", http.StatusConflict)
		return
	}

	if err := h.appendLog(id, "ANSWER", "human", toRole, response); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if h.Broker != nil {
		h.Broker.Publish(id, sse.LogEntryEvent{
			EntryType: "ANSWER",
			FromRole:  "human",
			ToRole:    toRole,
			Message:   response,
		})
	}

	var ticket db.Ticket
	if h.DB.First(&ticket, "id = ?", id).Error == nil && ticket.AssignedShem != nil {
		h.Hub.Push(*ticket.AssignedShem, ws.WSMessage{ //nolint:errcheck
			Type:     "human_input_resolved",
			TicketID: strPtr(id),
			InputID:  &hi.ID,
			Response: response,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// nextApprovalPhase returns the next phase for an approval transition.
func nextApprovalPhase(current string) (string, bool) {
	switch current {
	case "brainstorm":
		return "plan", true
	case "plan":
		return "implement", true
	}
	return "", false
}

// appendLog inserts a LogEntry for the given ticket, assigning the next sequence_num.
func (h *Handlers) appendLog(ticketID string, entryType, fromRole, toRole, message string) error {
	var maxSeq struct{ Max *uint }
	h.DB.Model(&db.LogEntry{}).
		Select("MAX(sequence_num) as max").
		Where("ticket_id = ?", ticketID).
		Scan(&maxSeq)
	var nextSeq uint = 1
	if maxSeq.Max != nil {
		nextSeq = *maxSeq.Max + 1
	}
	entry := db.LogEntry{
		TicketID:    ticketID,
		SequenceNum: nextSeq,
		EntryType:   entryType,
		FromRole:    fromRole,
		ToRole:      toRole,
		Message:     message,
		CreatedAt:   time.Now(),
	}
	return h.DB.Create(&entry).Error
}
