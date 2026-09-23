package ghsync

// CondenseAPIErrorForTest exposes condenseAPIError to the external test
// package, which is where the rest of this package's tests live.
func CondenseAPIErrorForTest(err error) string { return condenseAPIError(err) }
