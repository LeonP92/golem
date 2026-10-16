package epic

import (
	"reflect"
	"testing"
)

func readyKeys(m map[string]bool) []string {
	var out []string
	for _, k := range []string{"T1", "T2", "T3"} {
		if m[k] {
			out = append(out, k)
		}
	}
	return out
}

// Only an integrated predecessor unblocks; integrating T1 releases both
// successors; an integrated node is never ready again.
func TestReady(t *testing.T) {
	p := validPlan()
	for _, tc := range []struct {
		integrated map[string]bool
		want       []string
	}{
		{map[string]bool{}, []string{"T1"}},
		{map[string]bool{"T1": true}, []string{"T2", "T3"}},
		{map[string]bool{"T1": true, "T2": true}, []string{"T3"}},
	} {
		if got := readyKeys(Ready(p, tc.integrated)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Ready(%v) = %v, want %v", tc.integrated, got, tc.want)
		}
	}
}
