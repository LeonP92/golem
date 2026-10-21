# shem/config

`internal/shem/config` — parses `shem.yaml` into a `Config`.

Fields: `orchestrator`, `api_key` (overridable by `GOLEM_SHEM_API_KEY`),
`name` (by `GOLEM_SHEM_NAME`), `repos` with their remotes normalised,
`no_push`, `max_concurrent`, and `backend`.

`backend` is an `agentrunner.BackendConfig`, resolved by
`agentrunner.ResolveBackend` and checked by `Validate`. No block runs the
default adapter with its default catalog.

Per-repo `auto_retry` (absent = true) and `attention_fix_attempts` (absent = 3,
positive integer) configure automatic failure diagnosis. `AttentionPolicy`
resolves the host policy; a repository not managed by the shem is disabled.
