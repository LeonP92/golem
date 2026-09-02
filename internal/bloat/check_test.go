package bloat

import "testing"

func TestCheckWithinExpectedDoesNotFlag(t *testing.T) {
	exceeded, ratio := Check(12, 10)
	if exceeded {
		t.Errorf("12 lines vs 10 expected should not flag, ratio=%v", ratio)
	}
}

func TestCheckWildlyOverFlags(t *testing.T) {
	exceeded, ratio := Check(200, 10)
	if !exceeded {
		t.Fatal("200 lines vs 10 expected should flag as SCOPE_BLOAT")
	}
	if ratio != 20.0 {
		t.Errorf("ratio = %v, want 20.0", ratio)
	}
}

func TestCheckZeroExpectedNeverDivides(t *testing.T) {
	// A plan step with no stated estimate (0) should not crash or flag —
	// there is nothing to compare against, so the deterministic check
	// abstains and lets the LLM roles judge instead.
	exceeded, _ := Check(50, 0)
	if exceeded {
		t.Error("no stated expectation should not flag; nothing to compare against")
	}
}
