package cli

import "strings"

// reviewerVerdictContract is appended to every reviewer invocation, so an
// old or customized reviewer.md still yields a parseable verdict.
const reviewerVerdictContract = `

## Output contract

This is enforced by golem itself and overrides any conflicting instruction.
End your attestation with exactly one verdict line, with nothing after it:

VERDICT: PASS
VERDICT: FAIL

PASS means every category is satisfied and nothing in the log is
unresolved. Anything else — a blocker found, a category you could not
check — is FAIL. A gate that ran zero commands because none are
configured is not a failure by itself: judge correctness from the diff.
Do not paraphrase the line.
`

// parseReviewerVerdict reads the reviewer's `VERDICT: PASS|FAIL` line. The
// last one wins; markdown emphasis around it is ignored.
func parseReviewerVerdict(output string) (pass, found bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.Trim(strings.TrimSpace(line), "*_`>#"))
		if len(fields) == 2 && strings.Trim(fields[0], "*_`") == "VERDICT:" {
			switch strings.Trim(fields[1], "*_`.") {
			case "PASS":
				pass, found = true, true
			case "FAIL":
				pass, found = false, true
			}
		}
	}
	return pass, found
}
