package worker

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/leonp92/golem/internal/forge"
	"github.com/leonp92/golem/internal/shem/client"
	"github.com/leonp92/golem/internal/shem/config"
)

// IntegrateAssigned re-runs the integration lane for one assigned epic
// child. It is the Worker-facing entry to integrateEpicChild, for the
// startup sweep, periodic reconciliation and the resume push.
func (e *GolemExecutor) IntegrateAssigned(ctx context.Context, cfg *config.Config,
	c *client.Client, claim *client.ClaimResponse, repoPath, branch string) {
	e.integrateEpicChild(ctx, cfg, c, claim, repoPath, branch)
}

// integrationRemote is what the lane reads the child head from and pushes
// the merged target ref to. Normally that is the claim's remote. With
// pushes disabled, or for a local:// repository that has no remote at all,
// it is the local repository itself — the same place no_push routes the
// agent's own pushes (the executor points remote.origin.pushurl at the
// repo), so the merged epic branch shows up next to the ticket branches in
// git log --all and no network or credential is involved.
func integrationRemote(cfg *config.Config, claim *client.ClaimResponse, repoPath string) string {
	if cfg.NoPush || strings.HasPrefix(claim.RepoRemote, "local://") {
		return repoPath
	}
	return claim.RepoRemote
}

// integrateEpicChild runs the merge lane for a finished epic child: pin
// the remote head, pass the orchestrator's gate (which grants the per-run
// slot), merge into the run's target ref in the push mirror, and post the
// result. The receipt is idempotent end to end, so any interruption is
// recovered by simply running the lane again from the
// periodic assigned sweep, including after a shem restart.
func (e *GolemExecutor) integrateEpicChild(ctx context.Context, cfg *config.Config,
	c *client.Client, claim *client.ClaimResponse, repoPath, branch string) {
	if _, busy := e.integrating.LoadOrStore(claim.TicketID, struct{}{}); busy {
		return
	}
	defer e.integrating.Delete(claim.TicketID)
	ctx, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	ticketID := claim.TicketID
	ep := claim.Epic
	f := e.Forge
	if f == nil {
		f = forge.Git{}
	}
	mirror := pushMirrorPath(repoPath)
	remote := integrationRemote(cfg, claim, repoPath)

	// The lane remote is authoritative for the pin: the gate binds the
	// slot to the head the tester passed, which is what the remote (or, in
	// no_push mode, the shared local repository) holds, not what the
	// worktree happens to say now.
	head, err := f.HeadSHA(ctx, remote, branch)
	if err != nil {
		postStatus(c, ticketID, "Integration could not read the remote head: "+firstLineOf(err.Error()))
		log.Printf("executor: integrate %s: head: %v", ticketID, err)
		return
	}
	if head == "" {
		postStatus(c, ticketID, "Integration is waiting for branch "+branch+" at the merge source")
		return // publication may have failed; periodic reconciliation retries.
	}

	g, err := c.CheckIntegrationGate(ep.EpicID, ep.NodeKey, head)
	if err != nil {
		// Transient (the orchestrator is restarting, the ticket moved):
		// report nothing; the periodic assigned sweep re-runs the lane.
		postStatus(c, ticketID, "Integration gate check failed: "+firstLineOf(err.Error()))
		log.Printf("executor: integrate %s: gate: %v", ticketID, err)
		return
	}
	switch g.Outcome {
	case "done", "wait":
		return // normal polling reconciles paused or busy runs, without timers.
	case "granted":
	default:
		postStatus(c, ticketID, "Integration gate returned an unknown outcome")
		return
	}
	expires, err := time.Parse(time.RFC3339, g.ExpiresAt)
	if err != nil || !expires.After(time.Now()) || g.LeaseID == "" {
		postStatus(c, ticketID, "Integration gate returned an invalid or expired lease")
		return
	}
	mergeCtx, cancel := context.WithDeadline(ctx, expires)
	defer cancel()

	res, err := f.MergeIntoTarget(mergeCtx, forge.Merge{
		Mirror:    mirror,
		Remote:    remote,
		Branch:    branch,
		HeadSHA:   head,
		TargetRef: g.TargetRef,
		Message:   "Merge " + branch + " into " + g.TargetRef + " (" + ep.NodeKey + ")",
	})
	if err != nil {
		// Operational (the network, the git binary): nothing is reported,
		// the slot lapses on its own, and the periodic assigned sweep re-runs.
		postStatus(c, ticketID, "Integration merge failed: "+firstLineOf(err.Error()))
		log.Printf("executor: integrate %s: merge: %v", ticketID, err)
		return
	}

	result := client.IntegrationResult{LocalOnly: cfg.NoPush || strings.HasPrefix(claim.RepoRemote, "local://"), LeaseID: g.LeaseID, HeadSHA: head, Detail: res.Detail}
	switch res.Status {
	case forge.Merged:
		result.Status, result.MergeSHA = "merged", res.SHA
	case forge.Contained:
		result.Status, result.MergeSHA = "contained", res.SHA
	case forge.Conflict:
		result.Status = "conflict"
	case forge.Moved:
		result.Status = "moved"
	case forge.Rejected:
		result.Status = "error"
	}
	e.reportIntegration(c, ep, ticketID, result)
}

// reportIntegration posts the merge outcome. A failure to post leaves the
// merge on the remote and the receipt owed: the periodic assigned sweep re-runs the
// lane, the forge reports the head contained, and the receipt lands then.
func (e *GolemExecutor) reportIntegration(c *client.Client, ep *client.EpicClaim, ticketID string, r client.IntegrationResult) {
	if err := c.PostIntegrationResult(ep.EpicID, ep.NodeKey, r); err != nil {
		postStatus(c, ticketID, "Integration result could not be posted: "+firstLineOf(err.Error()))
		log.Printf("executor: integrate %s: post result: %v", ticketID, err)
		return
	}
	switch r.Status {
	case "merged":
		postStatus(c, ticketID, "Merged into the run's integration branch")
	case "conflict":
		postStatus(c, ticketID, "Integration conflicts; the ticket is back for revision")
	}
}
