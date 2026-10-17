package tester

import "testing"

func TestParseTesterVerdict(t *testing.T) {
	out := "I ran the app and tried the login flow.\n" +
		"TESTER-VERDICT: PASS\nHEAD: abc123\nBASE: def456\n" +
		"SCENARIOS: login flow, logout flow\nEVIDENCE: logs at /tmp/t.log\n"
	v, found := ParseVerdict(out)
	if !found || v.Verdict != "PASS" || v.HeadSHA != "abc123" || v.BaseSHA != "def456" ||
		v.Scenarios != "login flow, logout flow" || v.Evidence != "logs at /tmp/t.log" {
		t.Fatalf("v = %+v found=%t", v, found)
	}
}

func TestParseTesterVerdictMissingIsNotPassing(t *testing.T) {
	if _, found := ParseVerdict("looks good to me"); found {
		t.Fatal("no verdict line must read as not found")
	}
	if _, found := ParseVerdict("TESTER-VERDICT: MAYBE"); found {
		t.Fatal("an unreadable verdict must read as not found")
	}
}

// A tester that revised downward must have its last word stand.
func TestParseTesterVerdictLastOneWins(t *testing.T) {
	out := "TESTER-VERDICT: PASS\nHEAD: aaa\n" +
		"...then the app failed to start on re-run...\n" +
		"TESTER-VERDICT: INCONCLUSIVE\nHEAD: aaa\nBASE: bbb\nSCENARIOS: boot\nEVIDENCE: port in use\n"
	v, found := ParseVerdict(out)
	if !found || v.Verdict != "INCONCLUSIVE" {
		t.Fatalf("v = %+v", v)
	}
}
