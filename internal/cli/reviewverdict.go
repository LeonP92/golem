package cli

import "strings"

// parseReviewerVerdict reads the reviewer's `VERDICT: PASS|FAIL` line
// (see reviewerVerdictContract). The last one wins; markdown emphasis
// around it is ignored.
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
