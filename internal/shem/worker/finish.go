package worker

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/leonp92/golem/internal/blog"
	"github.com/leonp92/golem/internal/models"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
	"github.com/leonp92/golem/internal/ticket"
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
	worktree := filepath.Join(ticketDir, "worktree")
	if sha == "" {
		// State files do not carry a commit SHA. Capture the actual branch head
		// so a human retry can reconstruct this exact worktree on another shem.
		if head, err := revParse(ctx, worktree, "HEAD"); err == nil {
			sha = head
		}
	}

	// Written either way, and before the early return: the checkpoint lets an
	// explicit retry resume this phase instead of starting from brainstorm.
	if sha != "" && localPhase != "" {
		PostCheckpointWithRetry(c, ticketID, localPhase, sha, 5) //nolint:errcheck
	}

	if orchPhase != "ready-for-review" {
		// A failed gate is a real verdict, not a missing step. `golem ticket
		// review` writes needs-attention itself when the gate commands do not
		// pass, and reporting that as "did not complete" would blame the
		// agent for work the gate deliberately rejected.
		if localPhase == "needs-attention" {
			evidence := ""
			if entries, err := blog.ReadAll(filepath.Join(ticketDir, "log.jsonl")); err == nil {
				for i := len(entries) - 1; i >= 0; i-- {
					if entries[i].Role == "reviewer" || entries[i].Role == "gate" {
						evidence = entries[i].Message + "\n" + evidence
						if entries[i].Role == "gate" {
							break
						}
					}
				}
			}
			return fmt.Errorf("%s finished but the review gate did not pass; reviewer attestation and gate evidence:\n%s", what, evidence)
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
	if !cfg.NoPush && !strings.HasPrefix(claim.RepoRemote, "local://") {
		if pushErr := pushTicketBranch(ctx, repoPath, worktree, branch, claim.RepoRemote); pushErr != nil {
			if claim.Epic != nil {
				return fmt.Errorf("publishing epic child: %w", pushErr)
			}
			// A failed push must not block the ordinary ticket lifecycle: it still
			// reaches ready-for-review, just without a pull request.
			postStatus(c, ticketID, "Branch push failed, no pull request will be opened: "+pushErr.Error())
		} else {
			postStatus(c, ticketID, "Pushed "+branch+" to origin")
			// The first child to finish creates the integration ref at the
			// pinned base. Non-fatal: the lane creates a missing target at
			// the pinned head, so a later child retries none of this.
			if claim.Epic != nil {
				var baseSHA string
				if st, err := ticket.Load(ticketDir); err == nil {
					baseSHA = st.BranchBaseSHA
				}
				if baseSHA == "" {
					postStatus(c, ticketID, "No pinned base SHA recorded; leaving the "+
						"integration ref to another child")
				} else if err := ensureRemoteBaseRef(ctx, repoPath, integrationRemote(cfg, claim, repoPath),
					claim.Epic.BaseFallback, claim.BaseBranch, baseSHA); err != nil {
					postStatus(c, ticketID, "Could not create integration ref "+
						claim.BaseBranch+": "+firstLineOf(err.Error()))
					log.Printf("executor: ensure base ref for %s: %v", ticketID, err)
				}
			}
			// An epic child has no pull request, so no description is
			// generated for it; the branch report still lands, because
			// branch_pushed records that the lane has something to merge.
			if claim.Epic == nil {
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
			} else if pErr := c.PostBranchPushed(ticketID, ""); pErr != nil {
				log.Printf("executor: post branch-pushed: %v", pErr)
			}
		}
	}

	// The tester is a ticket-level stage: it runs for any ticket that
	// asked for it, with epics composing it by setting the flag on their
	// children. The claim's epic-scoped flag keeps the gate when the claim
	// comes from an orchestrator that predates the ticket-level flag; a
	// current orchestrator already ORs the run's policy into the top-level
	// field. None of this hangs off the push: a no_push or local:// run
	// merges from the local repository instead (integrationRemote), so an
	// epic child integrates there with no remote involved at all.
	testerRequired := claim.TesterRequired || (claim.Epic != nil && claim.Epic.TesterRequired)
	if claim.Epic != nil {
		// Epic child: the phase lands FIRST — the orchestrator's
		// verdict handling bounces a failed child out of
		// ready-for-review, so the child must be there before the
		// verdict arrives — then the tester, then this shem merges
		// the branch into the run's target ref itself.
		if phErr := c.PostPhase(ticketID, orchPhase); phErr != nil {
			if errors.Is(phErr, client.ErrNotOwner) {
				log.Printf("executor: ticket %s was requeued, stopping", ticketID)
				return nil
			}
			log.Printf("executor: post phase error: %v", phErr)
			return nil
		}
		verdict := ""
		if testerRequired {
			verdict = e.runTesterStage(ctx, c, claim, repoPath, worktree, ticketDir)
		}
		if testerRequired && verdict != "pass" {
			// The orchestrator bounced the child to revising or
			// parked it; the lane is not this shem's to take.
			return nil
		}
		e.integrateEpicChild(ctx, cfg, c, claim, repoPath, branch)
		return nil
	}
	if testerRequired {
		e.runTesterStage(ctx, c, claim, repoPath, worktree, ticketDir)
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
