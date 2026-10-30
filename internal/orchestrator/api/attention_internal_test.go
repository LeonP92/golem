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

func TestAttentionFingerprintIgnoresCountdowns(t *testing.T) {
	a := attentionFingerprint("You've hit your usage limit. Try again in 3 hours 20 minutes.")
	b := attentionFingerprint("You've hit your usage limit. Try again in 2 hours 5 minutes.")
	if a != b {
		t.Fatal("a shrinking countdown counted as a new failure")
	}
}
