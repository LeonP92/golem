package graph

import "testing"

func TestExtSet_includesDefaults(t *testing.T) {
	m := extSet(nil)
	for _, ext := range []string{".go", ".py", ".ts", ".rs"} {
		if !m[ext] {
			t.Errorf("expected %s in default extension set", ext)
		}
	}
}

func TestExtSet_includesExtras(t *testing.T) {
	m := extSet([]string{".graphql", ".proto"})
	if !m[".graphql"] || !m[".proto"] {
		t.Error("extra extensions not included")
	}
	if !m[".go"] {
		t.Error("defaults should still be present")
	}
}

func TestSkipped(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"node_modules/foo/bar.js", true},
		{"vendor/pkg/file.go", true},
		{"src/auth/auth.go", false},
		{"dist/bundle.js", true},
		{"src/dist/file.go", true},
	}
	skips := defaultSkips
	for _, c := range cases {
		if got := skipped(c.path, skips); got != c.want {
			t.Errorf("skipped(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
