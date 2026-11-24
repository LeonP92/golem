package db_test

import (
	"testing"
	"time"

	"github.com/leonp92/golem/internal/orchestrator/db"
)

func TestDisplayPhase(t *testing.T) {
	now := time.Now()
	future, past := now.Add(time.Hour), now.Add(-time.Hour)
	one := 7
	cases := []struct {
		name string
		t    db.Ticket
		want string
	}{
		{"active phase is untouched", db.Ticket{Phase: "implement", AttentionEligible: true}, "implement"},
		{"legacy parked row", db.Ticket{Phase: "needs-attention"}, "needs-attention"},
		{"eligible retry queued", db.Ticket{Phase: "needs-attention", AttentionEligible: true, UpdatedAt: now}, "recovering"},
		{"eligible retry unclaimed over 15 minutes", db.Ticket{Phase: "needs-attention", AttentionEligible: true, UpdatedAt: now.Add(-16 * time.Minute)}, "needs-attention"},
		{"same failure twice", db.Ticket{Phase: "needs-attention", AttentionReason: "Same failure twice; human attention required.", UpdatedAt: now}, "needs-attention"},
		{"diagnoser holds lease", db.Ticket{Phase: "needs-attention", AttentionEligible: true, AttentionToken: "x", AttentionLeaseUntil: &future}, "diagnosing"},
		{"expired lease falls back to queued retry", db.Ticket{Phase: "needs-attention", AttentionEligible: true, AttentionToken: "x", AttentionLeaseUntil: &past, UpdatedAt: now}, "recovering"},
		{"expired lease, not eligible", db.Ticket{Phase: "needs-attention", AttentionToken: "x", AttentionLeaseUntil: &past}, "needs-attention"},
		{"supervisor wait pending", db.Ticket{Phase: "needs-attention", AttentionWaitUntil: &future}, "recovering"},
		{"supervisor wait elapsed, human parked", db.Ticket{Phase: "needs-attention", AttentionWaitUntil: &past}, "needs-attention"},
		{"gave up (not eligible)", db.Ticket{Phase: "needs-attention", AttentionReason: "Stopping after 3 automatic recovery attempt(s)"}, "needs-attention"},
		{"manual approval is never auto-recovered", db.Ticket{Phase: "needs-attention", AttentionEligible: true, ManualApproval: true, UpdatedAt: now}, "needs-attention"},
		{"unapproved issue is never auto-recovered", db.Ticket{Phase: "needs-attention", AttentionEligible: true, IssueNumber: &one}, "needs-attention"},
		{"approved issue recovers", db.Ticket{Phase: "needs-attention", AttentionEligible: true, IssueNumber: &one, IntakeApproved: true, UpdatedAt: now}, "recovering"},
	}
	for _, c := range cases {
		if got := c.t.DisplayPhase(); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
