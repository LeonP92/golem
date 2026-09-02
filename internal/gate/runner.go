package gate

import (
	"os/exec"
	"strings"

	"github.com/leonpham/golem/internal/config"
)

type Result struct {
	Passed bool
	Output string
}

// Run stops at the first failing command rather than running the rest.
func Run(dir string, cfg config.GateConfig) (Result, error) {
	var output strings.Builder
	for _, command := range cfg.Commands {
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		output.Write(out)
		if err != nil {
			return Result{Passed: false, Output: output.String()}, nil
		}
	}
	return Result{Passed: true, Output: output.String()}, nil
}
