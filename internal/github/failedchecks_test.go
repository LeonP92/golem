package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/github"
)

// routes serves the three endpoints ListFailedChecks reads, with a status
// code per endpoint so a test can refuse one the way a token without that
// permission does.
type routes struct {
	checkRuns, workflowRuns, statuses string
	checkRunsCode, workflowRunsCode   int
	statusesCode                      int
}

func serve(t *testing.T, r routes) github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		body, code := "[]", http.StatusOK
		switch {
		case strings.Contains(req.URL.Path, "/check-runs"):
			body, code = r.checkRuns, r.checkRunsCode
		case strings.Contains(req.URL.Path, "/actions/runs"):
			body, code = r.workflowRuns, r.workflowRunsCode
		case strings.Contains(req.URL.Path, "/statuses"):
			body, code = r.statuses, r.statusesCode
		}
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := github.New("token", srv.URL+"/")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

const (
	failingCheckRun = `{"total_count":1,"check_runs":[{"name":"Python services","status":"completed","conclusion":"failure","html_url":"u"}]}`
	failingWorkflow = `{"total_count":1,"workflow_runs":[{"name":"Python services","status":"completed","conclusion":"failure","html_url":"u"}]}`
	failingStatus   = `[{"context":"ci/external","state":"failure","description":"boom","target_url":"u"}]`
	noStatuses      = `[]`
)

// A GitHub Actions job is BOTH a check run and a workflow run. Reading both
// sources — which a token with Checks and Actions does — must report it
// once, or every Actions failure is listed twice and the "N check(s)
// failing" count is doubled.
func TestListFailedChecks_DeduplicatesAcrossSources(t *testing.T) {
	c := serve(t, routes{checkRuns: failingCheckRun, workflowRuns: failingWorkflow, statuses: noStatuses})
	got, err := c.ListFailedChecks(context.Background(), "org", "repo", "sha")
	if err != nil {
		t.Fatalf("ListFailedChecks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d failures, want 1 (the same job from two sources): %+v", len(got), got)
	}
	if got[0].Name != "Python services" {
		t.Errorf("name = %q", got[0].Name)
	}
}

// Losing check runs alone loses nothing: workflow runs report the same
// Actions results. This is the deployed case — the fine-grained token is
// refused Checks but allowed Actions — and it must come back as a complete
// answer, not a partial one, or every pass reports reduced visibility that
// does not exist.
func TestListFailedChecks_ActionsCoversForRefusedCheckRuns(t *testing.T) {
	c := serve(t, routes{
		checkRuns:     `{"message":"Resource not accessible by personal access token"}`,
		checkRunsCode: http.StatusForbidden,
		workflowRuns:  failingWorkflow, statuses: noStatuses,
	})
	got, err := c.ListFailedChecks(context.Background(), "org", "repo", "sha")
	if err != nil {
		t.Fatalf("check runs were refused but workflow runs answered; want no error, got %v", err)
	}
	if len(got) != 1 || got[0].Name != "Python services" {
		t.Fatalf("the Actions route did not report the failure: %+v", got)
	}
}

// Losing BOTH views of Actions is a real blind spot and must be reported,
// while still returning what the remaining source did see.
func TestListFailedChecks_LosingBothActionsViewsIsPartial(t *testing.T) {
	forbidden := `{"message":"Resource not accessible by personal access token"}`
	c := serve(t, routes{
		checkRuns: forbidden, checkRunsCode: http.StatusForbidden,
		workflowRuns: forbidden, workflowRunsCode: http.StatusForbidden,
		statuses: failingStatus,
	})
	got, err := c.ListFailedChecks(context.Background(), "org", "repo", "sha")
	if err == nil {
		t.Fatal("both Actions views were refused and no error was reported; " +
			"an unknown number of failures would be invisible and the result read as complete")
	}
	if len(got) != 1 || got[0].Name != "ci/external" {
		t.Errorf("the readable source was discarded along with the refused ones: %+v", got)
	}
}
