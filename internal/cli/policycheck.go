package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/leonp92/golem/internal/config"
	"github.com/leonp92/golem/internal/gating"
)

// PolicyCheck is a vendor hook endpoint. A malformed event or policy blocks.
func PolicyCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("policy-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("policy", "", "host snapshot of policy JSON")
	root := fs.String("root", "", "allowed worktree root")
	noTools := fs.Bool("no-tools", false, "deny every tool")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return checkTool(os.Stdin, stderr, *path, *root, *noTools)
}
func checkTool(in io.Reader, errout io.Writer, path, root string, noTools bool) int {
	deny := func(reason string) int { fmt.Fprintln(errout, reason); return 2 }
	if noTools {
		return deny("diagnostic sessions cannot use tools")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return deny("cannot read tool policy")
	}
	var policy config.PolicyConfig
	if json.Unmarshal(data, &policy) != nil {
		return deny("invalid tool policy")
	}
	call, err := gating.DecodeCall(in)
	if err != nil {
		return deny("invalid tool event: " + err.Error())
	}
	d := gating.Check(policy, root, call)
	if !d.Allowed {
		return deny(d.Reason)
	}
	return 0
}
