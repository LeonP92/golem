package cli

import "strings"

// parseReviewerVerdict extracts the reviewer's final verdict from its
// output. The reviewer role (internal/roles/defaults/reviewer.md) must end
// its attestation with exactly one verdict line:
//
//	VERDICT: PASS
//	VERDICT: FAIL
//
// A review that blocks cannot be allowed to pass, and neither can one whose
// verdict is missing or unreadable: evidence that cannot be read is not a
// pass. When several verdict lines appear (the reviewer was told to emit
// one), the last one wins — a reviewer that first wrote PASS and then found
// a blocker must not have its earlier word stand.
func parseReviewerVerdict(output string) (pass, found bool) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "VERDICT:" {
			switch fields[1] {
			case "PASS":
				pass, found = true, true
			case "FAIL":
				pass, found = false, true
			}
		}
	}
	return pass, found
}
