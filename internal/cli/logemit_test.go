package cli

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/leonp92/golem/internal/blog"
)

func TestLogEmitAppendsEntry(t *testing.T) {
	repo := t.TempDir()
	ticketDir := filepath.Join(repo, ".golem", "tickets", "t1")

	var stdout, stderr bytes.Buffer
	code := LogEmit([]string{"--repo", repo, "--ticket", "t1", "--role", "convention-enforcer", "--type", "FINDING", "missing error check"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("LogEmit failed: exit %d, stderr=%s", code, stderr.String())
	}

	entries, err := blog.ReadAll(filepath.Join(ticketDir, "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != blog.TypeFinding || entries[0].Message != "missing error check" {
		t.Fatalf("got %+v", entries)
	}
}

func TestLogEmitRejectsQuestionAndAnswerTypes(t *testing.T) {
	repo := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := LogEmit([]string{"--repo", repo, "--ticket", "t1", "--role", "developer", "--type", "QUESTION", "should I do X?"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected LogEmit to reject QUESTION — that must go through `golem ask`")
	}
}

func TestAskThenAnswerRoundTrip(t *testing.T) {
	repo := t.TempDir()

	var askOut, askErr bytes.Buffer
	code := Ask([]string{"--repo", repo, "--ticket", "t1", "--from", "developer", "--to", "reviewer", "--timeout", "200ms", "should I extract a helper?"}, &askOut, &askErr)
	_ = code

	entries, err := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", "t1", "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var questionID string
	for _, e := range entries {
		if e.Type == blog.TypeQuestion {
			questionID = e.ID
		}
	}
	if questionID == "" {
		t.Fatal("expected a QUESTION entry to have been recorded")
	}

	var answerOut, answerErr bytes.Buffer
	code = Answer([]string{"--repo", repo, "--ticket", "t1", "--from", "reviewer", "--in-reply-to", questionID, "yes, extract it"}, &answerOut, &answerErr)
	if code != 0 {
		t.Fatalf("Answer failed: exit %d, stderr=%s", code, answerErr.String())
	}

	entries, err = blog.ReadAll(filepath.Join(repo, ".golem", "tickets", "t1", "log.jsonl"))
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Type == blog.TypeAnswer && e.InReplyTo == questionID && e.Message == "yes, extract it" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected an ANSWER entry replying to %s, got %+v", questionID, entries)
	}
}

func TestAskReturnsAnswerWhenPostedInTime(t *testing.T) {
	repo := t.TempDir()

	go func() {
		for i := 0; i < 20; i++ {
			time.Sleep(100 * time.Millisecond)
			entries, _ := blog.ReadAll(filepath.Join(repo, ".golem", "tickets", "t2", "log.jsonl"))
			for _, e := range entries {
				if e.Type == blog.TypeQuestion {
					Answer([]string{"--repo", repo, "--ticket", "t2", "--from", "reviewer", "--in-reply-to", e.ID, "yes"}, &bytes.Buffer{}, &bytes.Buffer{})
					return
				}
			}
		}
	}()

	var stdout, stderr bytes.Buffer
	code := Ask([]string{"--repo", repo, "--ticket", "t2", "--from", "developer", "--to", "reviewer", "--timeout", "2s", "ok to proceed?"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Ask failed: exit %d, stderr=%s", code, stderr.String())
	}
	if stdout.String() == "" {
		t.Fatal("expected the answer text on stdout")
	}
}
