package cli

// reviewerVerdictContract is appended to every reviewer invocation. The
// role file (reviewer.md) is owned by the target repo once unpacked and is
// never overwritten, so the machine-readable output contract cannot live
// only there: an install initialised before the contract existed, or a repo
// that customized the file, would otherwise emit attestations nothing can
// parse, and every review would fail closed to needs-attention.
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
A missing or unreadable verdict is treated as not passing, so do not
paraphrase the line.
`
