# internal/askwait

The askwait module implements a question-and-answer protocol over the blog log, allowing one agent role to post a question and another to poll or block-wait for the answer. It enforces a configurable cap on back-and-forth rounds to prevent infinite loops between agents, escalating to human intervention when the limit is reached.

## Functions

- func Ask(w *blog.Writer, from, to, question string) (string, error) — posts a QUESTION entry to the blog and returns its generated ID
- func PollForAnswer(logPath, questionID string) (*blog.Entry, error) — reads the blog and returns the ANSWER entry matching the given question ID, or nil if not yet present
- func WaitForAnswer(logPath, questionID string, timeout, pollInterval time.Duration) (answer string, found bool, err error) — polls for an answer in a loop until found or timeout elapses
- func RoundsSoFar(entries []blog.Entry, thread string) int — counts completed answer rounds for a thread to enforce MaxRounds

## Types

- MaxRounds — constant capping the number of question/answer back-and-forth rounds before forcing human escalation

## Imports

github.com/leonpham/golem/internal/blog
