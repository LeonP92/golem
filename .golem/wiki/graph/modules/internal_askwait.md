# internal/askwait

The askwait module implements a question-and-answer coordination protocol over the shared blog log, allowing one agent role to post a typed QUESTION entry and another to poll or block-wait until a matching ANSWER entry appears. It generates random IDs to correlate question/answer pairs, enforces a configurable round cap (MaxRounds=3) to prevent agents from looping indefinitely, and provides both a non-blocking poll and a timeout-bounded blocking wait so callers can choose their own concurrency model.

## Functions

- Ask
- PollForAnswer
- WaitForAnswer
- RoundsSoFar
- TestAskWritesQuestionEntry
- TestPollForAnswerFindsMatchingAnswer
- TestPollForAnswerNotFoundYet
- TestWaitForAnswerReturnsAsSoonAsAnswerAppears
- TestWaitForAnswerTimesOut
- TestRoundsSoFarCountsQuestionAnswerPairs

## Imports

crypto/rand, encoding/hex, time, github.com/leonp92/golem/internal/blog, path/filepath, testing
