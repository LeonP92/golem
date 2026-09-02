# cmd/golem

This is the main entry point for the golem CLI binary. It owns the top-level command dispatch table, groups commands for help output, and routes subcommands (ticket, wiki, log, observer, graph) to their respective handler functions in the internal/cli package. It also embeds the version string injected at release build time and implements the help command with per-command drill-down support.

## Functions

- func dispatch(args []string, stdout, stderr io.Writer) int — routes top-level CLI arguments to registered command handlers
- func main() — binary entry point; calls dispatch with os.Args and exits with the returned code

## Imports

internal/cli
