package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/leonp92/golem/internal/epic"
)

// EpicPlanValidate checks a plan file the way the orchestrator checks any
// plan entering the system — branch targets against the run's ref, then
// the full structural rules — so the planning agent can iterate against
// the same gate its draft will be re-checked by. No model is involved.
func EpicPlanValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("epic-plan validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	target := fs.String("target", "", "the ref every node's branch_target must equal (the run's integration ref or base branch)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 1 || *target == "" {
		fmt.Fprintln(stderr, "usage: golem epic-plan validate --target <ref> <plan.json>")
		return 1
	}
	data, err := os.ReadFile(fs.Arg(0)) //nolint:gosec
	if err != nil {
		fmt.Fprintf(stderr, "read plan: %v\n", err)
		return 1
	}
	canonical, errs := epic.CanonicalPlan(data, *target)
	if len(errs) > 0 {
		fmt.Fprintf(stdout, "%d problem(s):\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(stdout, "- %s\n", e)
		}
		return 1
	}
	var plan epic.Plan
	if err := json.Unmarshal([]byte(canonical), &plan); err != nil {
		fmt.Fprintf(stderr, "plan JSON does not parse: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "plan valid: %d requirement(s), %d node(s), %d edge(s)\n",
		len(plan.Requirements), len(plan.Nodes), len(plan.Edges))
	return 0
}
