# shem/config

`internal/shem/config` — parses `shem.yaml` into a `Config`.

Fields: `orchestrator`, `api_key` (overridable by `GOLEM_SHEM_API_KEY`),
`name` (by `GOLEM_SHEM_NAME`), `repos` with their remotes normalised,
`no_push`, `max_concurrent`, and `backend`.

`backend` is an `agentrunner.BackendConfig`. `Load` runs it through
`agentrunner.ResolveBackend` and `Validate`, so the seed rule lives in one
place: no block gives the embedded claude-code block; a block with no models
gets the seed catalog whole; a block that declares models is used as written.
Nothing from the seed is merged field by field into a declared catalog.
