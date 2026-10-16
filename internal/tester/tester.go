// Package tester owns the tester stage's verdict contract: the block the
// tester role must end its report with, and the parser that reads it.
// Both the CLI and the shem worker parse the same output, so the contract
// lives here once — two parsers is how the contract drifts.
package tester

import "strings"

// Contract is appended to the tester role prompt at invocation time, not
// only shipped in the role file: role files are unpacked once and owned
// by the repo, so an install initialized before this contract existed —
// or a repo that customized the file — would otherwise produce reports
// with no verdict and park every auto-merge. The parsing contract must
// not depend on an editable file.
const Contract = `

## Required report tail

End your report with exactly these lines, nothing after them:

    TESTER-VERDICT: PASS
    HEAD: <the exact head SHA you tested>
    BASE: <the base SHA you merged or compared against>
    SCENARIOS: <scenario names you ran, comma-separated>
    EVIDENCE: <one line: where the logs are, or what failed>

The verdict word is PASS, FAIL or INCONCLUSIVE. A verdict you could not
earn by running the candidate is INCONCLUSIVE, never a guess. These
lines are parsed mechanically: a missing or unreadable verdict is not
passing, and a head SHA that does not match the candidate is ignored.
`

// Verdict is the parsed report tail.
type Verdict struct {
	Verdict   string // PASS, FAIL or INCONCLUSIVE
	HeadSHA   string
	BaseSHA   string
	Scenarios string
	Evidence  string
}

// ParseVerdict extracts the verdict block. found reports whether a
// recognizable verdict line exists at all. When several blocks appear,
// the last one wins — a tester that first wrote PASS and then hit an
// environment failure must not have its earlier word stand.
func ParseVerdict(output string) (v Verdict, found bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val := strings.Join(fields[1:], " ")
		switch fields[0] {
		case "TESTER-VERDICT:":
			switch fields[1] {
			case "PASS", "FAIL", "INCONCLUSIVE":
				v, found = Verdict{Verdict: fields[1]}, true
			}
		case "HEAD:":
			if found {
				v.HeadSHA = fields[1]
			}
		case "BASE:":
			if found {
				v.BaseSHA = fields[1]
			}
		case "SCENARIOS:":
			if found {
				v.Scenarios = val
			}
		case "EVIDENCE:":
			if found {
				v.Evidence = val
			}
		}
	}
	return v, found
}
