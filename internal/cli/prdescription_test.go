package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leonp92/golem/internal/cli"
	"github.com/leonp92/golem/internal/roles"
)

// golem init only unpacks role files into a repository that has no .golem at
// all, so a repository initialised before this role shipped would never
// receive it — and the pull request description would silently fall back to
// the minimal body on exactly the repositories already in use. The embedded
// default covers that; a repository that has customised the role still wins.
func TestPRDescriptionRoleIsShippedAndFallsBack(t *testing.T) {
	t.Run("the role is in the shipped set", func(t *testing.T) {
		found := false
		for _, n := range roles.RoleNames {
			if n == "pr-description" {
				found = true
			}
		}
		if !found {
			t.Error("pr-description is not in roles.RoleNames, so golem init will not write it")
		}
		if _, err := roles.Defaults.ReadFile("defaults/pr-description.md"); err != nil {
			t.Errorf("the default is not embedded: %v", err)
		}
	})

	t.Run("the shipped default states the rules that matter", func(t *testing.T) {
		b, err := roles.Defaults.ReadFile("defaults/pr-description.md")
		if err != nil {
			t.Fatalf("read default: %v", err)
		}
		body := string(b)
		// These are the constraints that make a generated description
		// trustworthy rather than plausible-sounding.
		for _, want := range []string{
			"Never invent a number",
			"Screenshots",
			"Expected:",
			"Acceptance criteria",
			"Manual testing",
			"Automated tests",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("the role prompt does not mention %q", want)
			}
		}
	})

	t.Run("a missing ticket is refused rather than guessed at", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, ".golem"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		var out, errb bytes.Buffer
		code := cli.TicketPRDescription([]string{"--repo", repo, "--ticket", "nope"}, &out, &errb)
		if code == 0 {
			t.Error("a missing ticket returned success")
		}
		if out.Len() != 0 {
			t.Errorf("wrote a body for a ticket that does not exist: %q", out.String())
		}
	})

	t.Run("no ticket id is a usage error", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := cli.TicketPRDescription(nil, &out, &errb); code == 0 {
			t.Error("missing --ticket returned success")
		}
		if !strings.Contains(errb.String(), "usage:") {
			t.Errorf("no usage line: %q", errb.String())
		}
	})
}
