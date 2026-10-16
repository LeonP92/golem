package tester

import "strings"

// Contract is appended to every tester prompt, so an old or customized
// tester.md still yields a parseable verdict.
const Contract = `

## Required report tail

End your report with exactly these lines, nothing after them:

    TESTER-VERDICT: PASS
    HEAD: <the exact head SHA you tested>
    BASE: <the base SHA you merged or compared against>
    SCENARIOS: <what you checked, comma-separated>
    EVIDENCE: <one line: what passed or what failed>

The verdict word is PASS, FAIL or INCONCLUSIVE. A verdict the evidence
does not support is INCONCLUSIVE, never a guess. These
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

// ParseVerdict reads the verdict block; the last one wins.
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
