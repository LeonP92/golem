package github

import "testing"

// TestIssueTicketDescription pins the exact bytes an issue becomes. Both the
// CLI and the orchestrator's ingest go through this method, and the approval
// gate hashes its result, so the composition is a contract rather than a
// formatting preference: changing it re-hashes every linked ticket in every
// deployment and sends unclaimed approved tickets back for re-approval.
func TestIssueTicketDescription(t *testing.T) {
	tests := []struct {
		name  string
		title string
		body  string
		want  string
	}{
		{
			name:  "empty body uses the title alone",
			title: "Fix the bug",
			body:  "",
			want:  "Fix the bug",
		},
		{
			name:  "title and body joined by a blank line",
			title: "Fix the bug",
			body:  "Steps to reproduce",
			want:  "Fix the bug\n\nSteps to reproduce",
		},
		{
			name:  "a body that is only whitespace is still a body",
			title: "Fix the bug",
			body:  "\n",
			want:  "Fix the bug\n\n\n",
		},
		{
			name:  "both empty yields empty, which callers must handle",
			title: "",
			body:  "",
			want:  "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Issue{Title: tc.title, Body: tc.body}.TicketDescription()
			if got != tc.want {
				t.Errorf("TicketDescription() = %q, want %q", got, tc.want)
			}
		})
	}
}
