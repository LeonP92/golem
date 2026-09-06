# cmd/golem

This is the main entry point for the golem CLI binary. It owns the top-level command dispatch table, groups commands for help output, and routes subcommands (ticket, wiki, log, observer, graph) to their respective handler functions in the internal/cli package. It also embeds the version string injected at release build time and implements the help command with per-command drill-down support.

## Functions

- TestDispatchUnknownCommand
- TestDispatchNoArgs

## Imports

fmt, io, os, strings, github.com/leonp92/golem/internal/cli, bytes, testing
