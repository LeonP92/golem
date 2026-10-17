package worker

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
)

// approvalPollInterval is how often a shem asks the orchestrator whether a
// human has answered an approval request.
var approvalPollInterval = 3 * time.Second

// approvalAPI is the slice of the orchestrator client the approval gate uses.
type approvalAPI interface {
	GetPendingApproval(ticketID string) (*client.PendingInput, error)
	PostApprovalRequest(ticketID, prompt string) (uint, error)
	GetApproval(ticketID string, inputID uint) (*client.PendingInput, error)
}

type approvalOutcome int

const (
	// approvalGranted: a human approved; carry on to the next phase.
	approvalGranted approvalOutcome = iota
	// approvalChanges: a human asked for changes; their feedback is queued
	// and the phase should run again.
	approvalChanges
	// approvalGone: the request vanished, which is what a requeue does to a
	// ticket's human inputs. The shem no longer owns the ticket.
	approvalGone
)

// awaitApproval blocks until the approval request inputID is answered.
// Transient orchestrator errors are logged and retried; only a context
// cancellation, or an answer that is neither "approved" nor
// "changes_requested", ends the wait with an error. An unrecognised answer is
// an error rather than a pass, so the gate cannot be opened by anything but
// an explicit approval.
func awaitApproval(ctx context.Context, c approvalAPI, ticketID string, inputID uint) (approvalOutcome, error) {
	t := time.NewTicker(approvalPollInterval)
	defer t.Stop()
	for {
		in, err := c.GetApproval(ticketID, inputID)
		switch {
		case err != nil:
			log.Printf("executor: poll approval %d for %s: %v", inputID, ticketID, err)
		case in == nil:
			return approvalGone, nil
		case in.ResolvedAt != nil:
			resp := ""
			if in.Response != nil {
				resp = *in.Response
			}
			switch resp {
			case "approved":
				return approvalGranted, nil
			case "changes_requested":
				return approvalChanges, nil
			default:
				return 0, fmt.Errorf("approval %d resolved with unexpected response %q", inputID, resp)
			}
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-t.C:
		}
	}
}

// gatedPhase runs one phase (run) and, for a ticket in manual-approval mode,
// holds at a human approval gate afterwards. It returns approvalGranted when
// the shem should advance and approvalGone when it should stop quietly.
//
// In automatic mode run is the whole story: its own validation is the gate.
// In manual mode the validation still runs first (it is part of run), then a
// human must approve. If the human asks for changes the phase runs again and
// consumes their feedback. If the shem restarts while a request is open it
// goes straight back to waiting instead of re-running the phase.
func gatedPhase(ctx context.Context, c approvalAPI, ticketID string, manual bool,
	prompt string, status func(string), run func() error) (approvalOutcome, error) {
	for {
		var id uint
		if manual {
			// A failed check must not read as "nothing open": rerunning the
			// phase would post a second request beside the open one.
			p, err := c.GetPendingApproval(ticketID)
			if err != nil {
				return 0, fmt.Errorf("check for an open approval: %w", err)
			}
			if p != nil {
				id = p.ID
			}
		}
		if id == 0 {
			if err := run(); err != nil {
				return 0, err
			}
			if !manual {
				return approvalGranted, nil
			}
			var err error
			if id, err = c.PostApprovalRequest(ticketID, prompt); err != nil {
				// Never fall through as approved: a gate that cannot be
				// raised must fail the ticket, not open it.
				return 0, fmt.Errorf("request approval: %w", err)
			}
		}
		status("Waiting for human approval…")
		out, err := awaitApproval(ctx, c, ticketID, id)
		if err != nil {
			return 0, err
		}
		if out == approvalChanges {
			status("Changes requested — running this phase again")
			continue
		}
		if out == approvalGranted {
			status("Approved by a human — continuing") // evidence for the reviewer
		}
		return out, nil
	}
}
