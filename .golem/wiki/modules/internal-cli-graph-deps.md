# internal/cli — GraphDeps

`func GraphDeps(args []string, stdout, stderr io.Writer) int`

Lists all modules that a given module directly imports, according to the graph index.

Accepts a single positional `<module>` argument plus `--repo`. Loads the full graph index via `graph.LoadAllModuleGraphs` and finds the entry whose `Module` field matches the target (exact match first, then case-insensitive fallback). If no entry is found it prints "module <target> not found in graph index". If the entry exists but has no imports it prints "<target> has no recorded imports". Otherwise it prints each import one per line. Exits 0 in all non-error cases.

Registered in `graphDispatch` in `cmd/golem/main.go` under the subcommand name `deps`.
