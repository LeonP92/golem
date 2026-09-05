package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/leonp92/golem/internal/cli"
)

var version = "dev" // set by -ldflags="-X main.version=<tag>" at release build time

type commandEntry struct {
	fn   func(args []string, stdout, stderr io.Writer) int
	desc string
}

var commandTable = map[string]commandEntry{}

// commandGroups controls the order and grouping for `golem help`.
var commandGroups = []struct {
	label    string
	commands []string
}{
	{"init", []string{"init"}},
	{"ticket", []string{"ticket", "tickets"}},
	{"graph", []string{"graph"}},
	{"wiki", []string{"wiki"}},
	{"log", []string{"log"}},
	{"misc", []string{"ask", "answer", "observer", "version"}},
}

func init() {
	commandTable["version"] = commandEntry{
		fn:   func(_ []string, stdout, _ io.Writer) int { fmt.Fprintln(stdout, version); return 0 },
		desc: "Print the golem version.",
	}
	commandTable["init"] = commandEntry{fn: cli.Init, desc: "Initialise .golem in a repo."}
	commandTable["ticket"] = commandEntry{fn: ticketDispatch, desc: "Manage tickets: new, resume, close, advance, review, set-step, check-bloat."}
	commandTable["tickets"] = commandEntry{fn: cli.Tickets, desc: "List all tickets and their current phase."}
	commandTable["wiki"] = commandEntry{fn: wikiDispatch, desc: "Wiki commands: search, rebuild."}
	commandTable["ask"] = commandEntry{fn: cli.Ask, desc: "Prompt an agent and stream its response."}
	commandTable["answer"] = commandEntry{fn: cli.Answer, desc: "Resolve an ask-wait gate with an agent response."}
	commandTable["log"] = commandEntry{fn: logDispatch, desc: "Log commands: emit."}
	commandTable["observer"] = commandEntry{fn: observerDispatch, desc: "Observer commands: dispatch."}
	commandTable["graph"] = commandEntry{fn: graphDispatch, desc: "Graph commands: build, update, status, who-imports, check-boundary, deps."}
}

func helpCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		for _, g := range commandGroups {
			fmt.Fprintf(stdout, "\n  %s\n", strings.ToUpper(g.label))
			for _, name := range g.commands {
				e, ok := commandTable[name]
				if !ok {
					continue
				}
				fmt.Fprintf(stdout, "    %-12s  %s\n", name, e.desc)
			}
		}
		fmt.Fprintln(stdout)
		return 0
	}
	// help <command>: invoke with --help to trigger FlagSet usage output.
	e, ok := commandTable[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 1
	}
	return e.fn([]string{"--help"}, stdout, stderr)
}

func ticketDispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: golem ticket <new|resume|close|advance|review|set-step|check-bloat> [flags]")
		return 1
	}
	switch args[0] {
	case "new":
		return cli.TicketNew(args[1:], stdout, stderr)
	case "resume":
		return cli.TicketResume(args[1:], stdout, stderr)
	case "set-step":
		return cli.SetStep(args[1:], stdout, stderr)
	case "check-bloat":
		return cli.CheckBloat(args[1:], stdout, stderr)
	case "advance":
		return cli.TicketAdvance(args[1:], stdout, stderr)
	case "review":
		return cli.TicketReview(args[1:], stdout, stderr)
	case "close":
		return cli.TicketClose(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown ticket subcommand %q\n", args[0])
		return 1
	}
}

func wikiDispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: golem wiki <search|rebuild> [flags]")
		return 1
	}
	switch args[0] {
	case "search":
		return cli.WikiSearch(args[1:], stdout, stderr)
	case "rebuild":
		return cli.WikiRebuild(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown wiki subcommand %q\n", args[0])
		return 1
	}
}

func logDispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "emit" {
		fmt.Fprintln(stderr, "usage: golem log emit [flags] <message>")
		return 1
	}
	return cli.LogEmit(args[1:], stdout, stderr)
}

func observerDispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "dispatch" {
		fmt.Fprintln(stderr, "usage: golem observer dispatch [flags]")
		return 1
	}
	return cli.ObserverDispatch(args[1:], stdout, stderr)
}

func graphDispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: golem graph <build|update|status|who-imports|check-boundary|deps> [flags]")
		return 1
	}
	switch args[0] {
	case "build":
		return cli.GraphBuild(args[1:], stdout, stderr)
	case "update":
		return cli.GraphUpdate(args[1:], stdout, stderr)
	case "status":
		return cli.GraphStatus(args[1:], stdout, stderr)
	case "who-imports":
		return cli.GraphWhoImports(args[1:], stdout, stderr)
	case "check-boundary":
		return cli.GraphCheckBoundary(args[1:], stdout, stderr)
	case "deps":
		return cli.GraphDeps(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown graph subcommand %q\n", args[0])
		return 1
	}
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: golem <command> [flags]")
		return 1
	}
	if args[0] == "help" {
		return helpCommand(args[1:], stdout, stderr)
	}
	e, ok := commandTable[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 1
	}
	return e.fn(args[1:], stdout, stderr)
}

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}
