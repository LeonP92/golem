package askwait

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/leonp92/golem/internal/blog"
)

// MaxRounds caps back-and-forth on a single question thread before
// forcing human escalation (spec: Concurrency Model — "a capped number
// of back-and-forth rounds (3)").
const MaxRounds = 3

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Ask posts a QUESTION entry and returns its ID, which the asker later
// passes to PollForAnswer.
func Ask(w *blog.Writer, from, to, question string) (string, error) {
	id := newID()
	e := blog.NewEntry(from, blog.TypeQuestion, question)
	e.ID = id
	e.Target = to
	return id, w.Append(e)
}

// PollForAnswer looks for an ANSWER entry whose InReplyTo matches
// questionID. The caller (the observer, or the developer role's own
// step loop) is responsible for calling this repeatedly with its own
// backoff until the configured per-backend timeout elapses.
// Returns the matching Entry pointer when found, nil entry when not found yet.
func PollForAnswer(logPath, questionID string) (*blog.Entry, error) {
	entries, err := blog.ReadAll(logPath)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Type == blog.TypeAnswer && entries[i].InReplyTo == questionID {
			return &entries[i], nil
		}
	}
	return nil, nil
}

func WaitForAnswer(logPath, questionID string, timeout, pollInterval time.Duration) (answer string, found bool, err error) {
	deadline := time.Now().Add(timeout)
	for {
		entry, err := PollForAnswer(logPath, questionID)
		if err != nil {
			return "", false, err
		}
		if entry != nil {
			return entry.Message, true, nil
		}
		if time.Now().After(deadline) {
			return "", false, nil
		}
		time.Sleep(pollInterval)
	}
}

// RoundsSoFar counts QUESTION/ANSWER pairs sharing the same thread
// (Target), used to enforce MaxRounds before escalating to a human
// rather than looping indefinitely.
func RoundsSoFar(entries []blog.Entry, thread string) int {
	rounds := 0
	for _, e := range entries {
		if e.Type == blog.TypeAnswer && e.Target == thread {
			rounds++
		}
	}
	return rounds
}
