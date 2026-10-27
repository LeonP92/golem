package gate

import (
	"fmt"
	"strings"

	"github.com/leonp92/golem/internal/evidence"
)

// Failure preserves command identity and an exit status without treating test
// prose containing words such as quota or unauthorized as an environment fault.
type Failure struct {
	Kind     string
	Command  string
	ExitCode int
}

func (f *Failure) String() string {
	return fmt.Sprintf("%s command=%q exit=%d", f.Kind, f.Command, f.ExitCode)
}

// EnvironmentOutput accepts the marker only at the start of the text or on the line after a "review gate could not run" wrapper,
// never inside test output, which an agent controls.
func EnvironmentOutput(text string) bool {
	text = strings.TrimSpace(text)
	first, rest, _ := strings.Cut(text, "\n")
	return strings.HasPrefix(text, evidence.EnvironmentMarker) ||
		strings.HasPrefix(first, "review gate could not run") && strings.HasPrefix(rest, evidence.EnvironmentMarker)
}
