package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"

	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// finishWorkPhase closes out an implement or revise run: it reads what the
// agent actually left behind, reports that, and only then moves the ticket.
//
// Reading the local state first matters: an agent that stopped before
// `golem ticket review` leaves an earlier phase there, and posting that would
// move the ticket backwards. An agent that did not advance the local ticket has
// not finished, so this returns an error and the ticket parks in
// needs-attention with the reason attached.
func (e *GolemExecutor) finishWorkPhase(ctx context.Context, cfg *config.Config, c *client.Client,
	claim *client.ClaimResponse, repoPath, ticketDir, what, branch string,
) error {
	ticketID := claim.TicketID

	localPhase, sha, stateErr := readState(ticketDir)
	orchPhase := toOrchestratorPhase(localPhase)

	// Written either way, and before the early return: the checkpoint is what
	// lets a requeue resume this phase instead of starting the ticket over
	// from brainstorm.
	if sha != "" {
		PostCheckpointWithRetry(c, ticketID, localPhase, sha, 5) //nolint:errcheck
	}

	if orchPhase != "ready-for-review" {
		// A failed gate is a real verdict, not a missing step. `golem ticket
		// review` writes needs-attention itself when the gate commands do not
		// pass, and reporting that as "did not complete" would blame the
		// agent for work the gate deliberately rejected.
		if localPhase == "needs-attention" {
			return fmt.Errorf("%s finished but the review gate did not pass — "+
				"see the reviewer attestation in the log above", what)
		}
		// The local phase is named because it is the whole diagnosis: "plan"
		// means the agent stopped at the planning gate, "implement" means the
		// review gate did not run or did not reach a verdict.
		if stateErr != nil {
			return fmt.Errorf("%s did not complete: no readable ticket state in %s (%v), "+
				"so there is nothing to review", what, ticketDir, stateErr)
		}
		return fmt.Errorf("%s did not complete: the agent left the local ticket at phase %q "+
			"instead of advancing it, so it is not ready for review — see the log above for "+
			"what it was waiting on", what, localPhase)
	}

	postStatus(c, ticketID, what+" complete — ready for review")

	if !cfg.NoPush {
		worktree := filepath.Join(ticketDir, "worktree")
		if pushErr := pushTicketBranch(ctx, repoPath, worktree, branch, claim.RepoRemote); pushErr != nil {
			// A failed push must not block the lifecycle: the ticket still
			// reaches ready-for-review, just without a pull request.
			postStatus(c, ticketID, "Branch push failed, no pull request will be opened: "+pushErr.Error())
		} else {
			postStatus(c, ticketID, "Pushed "+branch+" to origin")
			// Generated here, after the push and before the report, because
			// the description is written from the branch's own diff and only
			// this host has it. A failure is logged and the report goes out
			// anyway: the orchestrator falls back to a minimal body, so a
			// missing description costs a good write-up, not the pull
			// request.
			prBody, bodyErr := e.generatePRDescription(ctx, repoPath, ticketID, e.claimModel(c, claim, models.StagePRDescription))
			if bodyErr != nil {
				postStatus(c, ticketID, "Could not generate the pull request description: "+
					firstLineOf(bodyErr.Error()))
				log.Printf("executor: pr description for %s: %v", ticketID, bodyErr)
			}
			if pErr := c.PostBranchPushed(ticketID, prBody); pErr != nil {
				log.Printf("executor: post branch-pushed: %v", pErr)
			}
		}
	}

	if phErr := c.PostPhase(ticketID, orchPhase); phErr != nil {
		if errors.Is(phErr, client.ErrNotOwner) {
			log.Printf("executor: ticket %s was requeued, stopping", ticketID)
			return nil
		}
		log.Printf("executor: post phase error: %v", phErr)
	}
	return nil
}
