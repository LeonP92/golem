# shem/config

`internal/shem/config` — parses `shem.yaml` into a `Config`.

Fields: `orchestrator`, `api_key` (overridable by `GOLEM_SHEM_API_KEY`),
`name` (by `GOLEM_SHEM_NAME`), `repos` with their remotes normalised,
`no_push`, `max_concurrent`, and `backend`.

`backend` is an `agentrunner.BackendConfig`, resolved by
`agentrunner.ResolveBackend` and checked by `Validate`. No block runs the
default adapter with its default catalog.
