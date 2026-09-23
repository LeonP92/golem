package main

import (
	"strings"
	"testing"
)

// TestGitHubSyncPlan pins the startup decision, whose defect was that it was
// made once and never revisited: a fresh install has zero enabled repos, so
// the worker was never constructed, so enabling the first repository through
// the UI did nothing until someone restarted the orchestrator. The settings
// page meanwhile says "Golem polls every 15 minutes".
//
// The fix is the third row: a token is enough to start the worker, and
// Worker.ingestAll re-reads the enabled repos on every pass, so a repo
// enabled at runtime is picked up by the next poll — or immediately by
// Sync now, which resolves the repo by ID at trigger time.
func TestGitHubSyncPlan(t *testing.T) {
	tests := []struct {
		name         string
		token        string
		enabledRepos int64
		wantStart    bool
		wantLogHas   string
	}{
		{
			name:         "no token and no repos stays off",
			token:        "",
			enabledRepos: 0,
			wantStart:    false,
			wantLogHas:   "GOLEM_GITHUB_TOKEN is empty",
		},
		{
			name:         "no token with repos enabled is an error, still off",
			token:        "",
			enabledRepos: 2,
			wantStart:    false,
			wantLogHas:   "ERROR",
		},
		{
			name:         "token with no repos starts anyway",
			token:        "ghp_x",
			enabledRepos: 0,
			wantStart:    true,
			wantLogHas:   "no repositories enabled",
		},
		{
			name:         "token with repos starts",
			token:        "ghp_x",
			enabledRepos: 3,
			wantStart:    true,
			wantLogHas:   "3 repo",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := githubSyncPlan("GOLEM_GITHUB_TOKEN", tc.token, tc.enabledRepos)
			if plan.start != tc.wantStart {
				t.Errorf("start = %v, want %v (log: %s)", plan.start, tc.wantStart, plan.log)
			}
			if plan.log == "" {
				t.Fatal("log is empty; every startup decision must say something")
			}
			if !strings.Contains(plan.log, tc.wantLogHas) {
				t.Errorf("log = %q, want it to contain %q", plan.log, tc.wantLogHas)
			}
		})
	}
}

// TestGitHubSyncPlanNeverMentionsTheTokenValue guards the one thing this
// function must not do with the argument it is handed.
func TestGitHubSyncPlanNeverMentionsTheTokenValue(t *testing.T) {
	const secret = "ghp_thisisasecretvalue"
	for _, repos := range []int64{0, 1} {
		plan := githubSyncPlan("GOLEM_GITHUB_TOKEN", secret, repos)
		if strings.Contains(plan.log, secret) {
			t.Fatalf("startup log leaks the token: %q", plan.log)
		}
	}
}
