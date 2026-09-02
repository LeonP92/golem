package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leonpham/golem/internal/askwait"
	"github.com/leonpham/golem/internal/blog"
)

var emittableTypes = map[string]blog.EntryType{
	"STATUS":   blog.TypeStatus,
	"FINDING":  blog.TypeFinding,
	"BLOCKER":  blog.TypeBlocker,
	"RESOLVED": blog.TypeResolved,
}

func LogEmit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("log emit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	role := fs.String("role", "", "role emitting this entry (required)")
	typ := fs.String("type", "", "STATUS|FINDING|BLOCKER|RESOLVED (required)")
	target := fs.String("target", "", "role this entry addresses, if any")
	inReplyTo := fs.String("in-reply-to", "", "id of the entry this resolves, if any")
	commitSHA := fs.String("commit-sha", "", "commit this entry concerns, if any")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	message := strings.Join(fs.Args(), " ")
	if *id == "" || *role == "" || message == "" {
		fmt.Fprintln(stderr, "usage: golem log emit --ticket <id> --role <role> --type <type> <message>")
		return 1
	}
	entryType, ok := emittableTypes[*typ]
	if !ok {
		fmt.Fprintf(stderr, "unsupported type %q; QUESTION/ANSWER go through `golem ask`/`golem answer`, valid types here: STATUS, FINDING, BLOCKER, RESOLVED\n", *typ)
		return 1
	}

	logPath := filepath.Join(*repo, ".golem", "tickets", *id, "log.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "creating ticket dir: %v\n", err)
		return 1
	}
	w, err := blog.NewWriter(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "opening log: %v\n", err)
		return 1
	}
	defer w.Close()

	e := blog.NewEntry(*role, entryType, message)
	e.Target = *target
	e.InReplyTo = *inReplyTo
	e.CommitSHA = *commitSHA
	if err := w.Append(e); err != nil {
		fmt.Fprintf(stderr, "writing entry: %v\n", err)
		return 1
	}
	return 0
}

func Ask(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	from := fs.String("from", "", "role asking (required)")
	to := fs.String("to", "", "role being asked (required)")
	timeoutStr := fs.String("timeout", "5m", "how long to wait for an answer")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	question := strings.Join(fs.Args(), " ")
	if *id == "" || *from == "" || *to == "" || question == "" {
		fmt.Fprintln(stderr, "usage: golem ask --ticket <id> --from <role> --to <role> <question>")
		return 1
	}
	timeout, err := time.ParseDuration(*timeoutStr)
	if err != nil {
		fmt.Fprintf(stderr, "invalid --timeout: %v\n", err)
		return 1
	}

	logPath := filepath.Join(*repo, ".golem", "tickets", *id, "log.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "creating ticket dir: %v\n", err)
		return 1
	}
	entries, err := blog.ReadAll(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "reading log: %v\n", err)
		return 1
	}
	if askwait.RoundsSoFar(entries, *to) >= askwait.MaxRounds {
		fmt.Fprintf(stderr, "round cap reached for thread %q — escalate to a human instead of asking again\n", *to)
		return 1
	}

	w, err := blog.NewWriter(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "opening log: %v\n", err)
		return 1
	}
	questionID, err := askwait.Ask(w, *from, *to, question)
	w.Close()
	if err != nil {
		fmt.Fprintf(stderr, "posting question: %v\n", err)
		return 1
	}

	answer, found, err := askwait.WaitForAnswer(logPath, questionID, timeout, 2*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "waiting for answer: %v\n", err)
		return 1
	}
	if !found {
		w, _ := blog.NewWriter(logPath)
		timeoutEntry := blog.NewEntry("system", blog.TypeTimeout, "no answer within timeout")
		timeoutEntry.InReplyTo = questionID
		w.Append(timeoutEntry)
		w.Close()
		fmt.Fprintf(stderr, "timed out waiting for an answer from %s\n", *to)
		return 1
	}

	fmt.Fprintln(stdout, answer)
	return 0
}

func Answer(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("answer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "target repo root")
	id := fs.String("ticket", "", "ticket id (required)")
	from := fs.String("from", "", "role answering (required)")
	inReplyTo := fs.String("in-reply-to", "", "id of the question being answered (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	message := strings.Join(fs.Args(), " ")
	if *id == "" || *from == "" || *inReplyTo == "" || message == "" {
		fmt.Fprintln(stderr, "usage: golem answer --ticket <id> --from <role> --in-reply-to <id> <answer>")
		return 1
	}

	logPath := filepath.Join(*repo, ".golem", "tickets", *id, "log.jsonl")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		fmt.Fprintf(stderr, "creating ticket dir: %v\n", err)
		return 1
	}
	entries, err := blog.ReadAll(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "reading log: %v\n", err)
		return 1
	}
	var thread string
	for _, e := range entries {
		if e.Type == blog.TypeQuestion && e.ID == *inReplyTo {
			thread = e.Target
		}
	}

	w, err := blog.NewWriter(logPath)
	if err != nil {
		fmt.Fprintf(stderr, "opening log: %v\n", err)
		return 1
	}
	defer w.Close()
	e := blog.NewEntry(*from, blog.TypeAnswer, message)
	e.InReplyTo = *inReplyTo
	e.Target = thread
	return boolToExit(w.Append(e))
}

func boolToExit(err error) int {
	if err != nil {
		return 1
	}
	return 0
}
