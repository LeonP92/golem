package bloat

// scopeBloatMultiplier is the threshold past which a diff is flagged
// as SCOPE_BLOAT — a cheap, deterministic backstop that needs no agent
// judgment (spec: Ticket Lifecycle, step 4). Fixed per KISS/YAGNI; not
// exposed as a config knob until a real case shows it needs to vary.
const scopeBloatMultiplier = 3.0

// Check compares a commit's actual diff line count against the plan
// step's stated expectation. expectedLines == 0 means the plan step
// gave no estimate, in which case the deterministic check abstains
// (returns false) and leaves scope judgment to the LLM roles.
func Check(actualLines, expectedLines int) (exceeded bool, ratio float64) {
	if expectedLines == 0 {
		return false, 0
	}
	ratio = float64(actualLines) / float64(expectedLines)
	return ratio > scopeBloatMultiplier, ratio
}
