// Package prereq checks external capabilities before work starts.
package prereq

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/leonp92/golem/internal/evidence"
)

// Git checks the capability Golem needs rather than guessing from vendor
// version suffixes. check-attr -h exits with usage status even when supported.
func Git() error {
	out, _ := exec.Command("git", "check-attr", "-h").CombinedOutput()
	if !strings.Contains(string(out), "--source") {
		version, _ := exec.Command("git", "--version").Output()
		return fmt.Errorf("%s Git >=2.40 with check-attr --source is required (found %s)", evidence.EnvironmentMarker, strings.TrimSpace(string(version)))
	}
	return nil
}
