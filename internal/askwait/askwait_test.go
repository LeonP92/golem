package askwait

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/leonpham/golem/internal/blog"
)

func TestAskWritesQuestionEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	w, err := blog.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	id, err := Ask(w, "developer", "reviewer", "should I extract this into a helper?")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty question id")
	}

	entries, err := blog.ReadAll(path)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 1 || entries[0].Type != blog.TypeQuestion {
		t.Fatalf("expected one QUESTION entry, got %+v", entries)
	}
	if entries[0].ID != id {
		t.Errorf("entry ID = %q, want %q", entries[0].ID, id)
	}
}

func TestPollForAnswerFindsMatchingAnswer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	w, err := blog.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	id, _ := Ask(w, "developer", "reviewer", "question?")
	answer := blog.NewEntry("reviewer", blog.TypeAnswer, "yes, extract it")
	answer.InReplyTo = id
	if err := w.Append(answer); err != nil {
		t.Fatalf("Append answer: %v", err)
	}

	entry, err := PollForAnswer(path, id)
	if err != nil {
		t.Fatalf("PollForAnswer: %v", err)
	}
	if entry == nil {
		t.Fatal("expected to find the answer")
	}
	if entry.InReplyTo != id {
		t.Errorf("entry.InReplyTo = %q, want %q", entry.InReplyTo, id)
	}
	if entry.Message != "yes, extract it" {
		t.Errorf("entry.Message = %q, want %q", entry.Message, "yes, extract it")
	}
}

func TestPollForAnswerNotFoundYet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	w, err := blog.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	id, _ := Ask(w, "developer", "reviewer", "question?")

	entry, err := PollForAnswer(path, id)
	if err != nil {
		t.Fatalf("PollForAnswer: %v", err)
	}
	if entry != nil {
		t.Fatal("expected not found — no answer has been appended yet")
	}
}

func TestWaitForAnswerReturnsAsSoonAsAnswerAppears(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	w, err := blog.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	id, _ := Ask(w, "developer", "reviewer", "question?")
	w.Close()

	go func() {
		time.Sleep(30 * time.Millisecond)
		w2, _ := blog.NewWriter(path)
		answer := blog.NewEntry("reviewer", blog.TypeAnswer, "yes")
		answer.InReplyTo = id
		w2.Append(answer)
		w2.Close()
	}()

	answer, found, err := WaitForAnswer(path, id, 500*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForAnswer: %v", err)
	}
	if !found || answer != "yes" {
		t.Fatalf("got found=%v answer=%q, want found=true answer=yes", found, answer)
	}
}

func TestWaitForAnswerTimesOut(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.jsonl")
	w, err := blog.NewWriter(path)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	id, _ := Ask(w, "developer", "reviewer", "question?")
	w.Close()

	_, found, err := WaitForAnswer(path, id, 50*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("WaitForAnswer: %v", err)
	}
	if found {
		t.Fatal("expected found=false — no answer was ever posted")
	}
}

func TestRoundsSoFarCountsQuestionAnswerPairs(t *testing.T) {
	entries := []blog.Entry{
		{Type: blog.TypeQuestion, ID: "q1", Target: "thread-1"},
		{Type: blog.TypeAnswer, InReplyTo: "q1", Target: "thread-1"},
		{Type: blog.TypeQuestion, ID: "q2", Target: "thread-1"},
		{Type: blog.TypeAnswer, InReplyTo: "q2", Target: "thread-1"},
		{Type: blog.TypeQuestion, ID: "q3", Target: "thread-other"},
	}
	rounds := RoundsSoFar(entries, "thread-1")
	if rounds != 2 {
		t.Errorf("RoundsSoFar = %d, want 2", rounds)
	}
}
