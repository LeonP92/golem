package urlnorm_test

import (
	"testing"

	"github.com/leonp92/golem/internal/orchestrator/urlnorm"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/org/repo.git", "https://github.com/org/repo"},
		{"https://github.com/org/repo", "https://github.com/org/repo"},
		{"HTTPS://GitHub.com/org/repo.git", "https://github.com/org/repo"},
		{"https://github.com/org/repo/", "https://github.com/org/repo"},
	}
	for _, c := range cases {
		if got := urlnorm.Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
