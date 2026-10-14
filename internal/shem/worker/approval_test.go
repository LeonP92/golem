package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/shem/client"
)

type fakeApprovals struct {
	pending  *client.PendingInput
	pendErr  error
	postErr  error
	posted   int
	answers  []string // consumed one per post: resolved response
	resolved map[uint]string
	gone     bool
	nextID   uint
}

func (f *fakeApprovals) GetPendingApproval(string) (*client.PendingInput, error) {
	return f.pending, f.pendErr
}

func (f *fakeApprovals) PostApprovalRequest(string, string) (uint, error) {
	if f.postErr != nil {
		return 0, f.postErr
	}
	f.nextID++
	f.posted++
	if f.resolved == nil {
		f.resolved = map[uint]string{}
	}
	f.resolved[f.nextID] = f.answers[f.posted-1]
	return f.nextID, nil
}

func (f *fakeApprovals) GetApproval(_ string, id uint) (*client.PendingInput, error) {
	if f.gone {
		return nil, nil
	}
	now := time.Now()
	resp := f.resolved[id]
	return &client.PendingInput{ID: id, ResolvedAt: &now, Response: &resp}, nil
}

func fast(t *testing.T) {
	old := approvalPollInterval
	approvalPollInterval = time.Millisecond
	t.Cleanup(func() { approvalPollInterval = old })
}

func noStatus(string) {}

func TestAutomaticModeRunsOnceAndNeverAsks(t *testing.T) {
	fast(t)
	f := &fakeApprovals{}
	runs := 0
	out, err := gatedPhase(context.Background(), f, "T", false, "p", noStatus, func() error { runs++; return nil })
	if err != nil || out != approvalGranted || runs != 1 || f.posted != 0 {
		t.Fatalf("out=%v err=%v runs=%d posted=%d", out, err, runs, f.posted)
	}
}

func TestManualModeWaitsForApproval(t *testing.T) {
	fast(t)
	f := &fakeApprovals{answers: []string{"approved"}}
	runs := 0
	out, err := gatedPhase(context.Background(), f, "T", true, "p", noStatus, func() error { runs++; return nil })
	if err != nil || out != approvalGranted || runs != 1 || f.posted != 1 {
		t.Fatalf("out=%v err=%v runs=%d posted=%d", out, err, runs, f.posted)
	}
}

func TestManualModeRerunsOnRequestedChanges(t *testing.T) {
	fast(t)
	f := &fakeApprovals{answers: []string{"changes_requested", "approved"}}
	runs := 0
	out, err := gatedPhase(context.Background(), f, "T", true, "p", noStatus, func() error { runs++; return nil })
	if err != nil || out != approvalGranted || runs != 2 || f.posted != 2 {
		t.Fatalf("out=%v err=%v runs=%d posted=%d", out, err, runs, f.posted)
	}
}

func TestManualModeResumesAnOpenRequestWithoutRerunning(t *testing.T) {
	fast(t)
	f := &fakeApprovals{pending: &client.PendingInput{ID: 7}, resolved: map[uint]string{7: "approved"}}
	runs := 0
	out, err := gatedPhase(context.Background(), f, "T", true, "p", noStatus, func() error { runs++; return nil })
	if err != nil || out != approvalGranted || runs != 0 {
		t.Fatalf("out=%v err=%v runs=%d", out, err, runs)
	}
}

func TestAnUnraisableGateFailsInsteadOfOpening(t *testing.T) {
	fast(t)
	f := &fakeApprovals{postErr: errors.New("boom")}
	_, err := gatedPhase(context.Background(), f, "T", true, "p", noStatus, func() error { return nil })
	if err == nil {
		t.Fatal("manual gate that could not be raised must not pass")
	}
}

func TestAFailedOpenRequestCheckDoesNotRerunThePhase(t *testing.T) {
	fast(t)
	f := &fakeApprovals{pendErr: errors.New("boom"), answers: []string{"approved"}}
	runs := 0
	_, err := gatedPhase(context.Background(), f, "T", true, "p", noStatus, func() error { runs++; return nil })
	if err == nil || runs != 0 || f.posted != 0 {
		t.Fatalf("err=%v runs=%d posted=%d", err, runs, f.posted)
	}
}

func TestAnUnrecognisedAnswerIsNotAnApproval(t *testing.T) {
	fast(t)
	f := &fakeApprovals{answers: []string{"acknowledged"}}
	_, err := gatedPhase(context.Background(), f, "T", true, "p", noStatus, func() error { return nil })
	if err == nil {
		t.Fatal("only an explicit approval may open the gate")
	}
}

func TestARequeuedTicketStopsTheWait(t *testing.T) {
	fast(t)
	f := &fakeApprovals{answers: []string{"approved"}, gone: true}
	out, err := gatedPhase(context.Background(), f, "T", true, "p", noStatus, func() error { return nil })
	if err != nil || out != approvalGone {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

func TestCancellationEndsTheWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := awaitApproval(ctx, &neverResolved{}, "T", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

type neverResolved struct{ fakeApprovals }

func (n *neverResolved) GetApproval(_ string, id uint) (*client.PendingInput, error) {
	return &client.PendingInput{ID: id}, nil
}
