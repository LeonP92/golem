package api

import "testing"

func TestAttentionFingerprintIgnoresOnlyVolatileEvidence(t *testing.T) {
	a := attentionFingerprint("FAIL at head abcdef1234567890 on 2026-10-20T11:00:00Z: expected 200 got 500")
	b := attentionFingerprint("FAIL at head 1234567890abcdef on 2026-10-20T12:00:00Z: expected 200 got 500")
	if a != b {
		t.Fatal("same failure at different head counted as new")
	}
	c := attentionFingerprint("FAIL at head abcdef1234567890 on 2026-10-20T11:00:00Z: expected 200 got 404")
	if a == c {
		t.Fatal("different assertion treated as same")
	}
}
