# agentrunner

`internal/agentrunner` — the backend adapter layer: one vendor CLI per
adapter, behind a registry.

## Interfaces

- `Runner` — `Name`, `RunAgent(ctx, role, in, model)`, `RunPhase(ctx, dir,
  prompt, out, model)`.
- `Adapter` — `Runner` plus the on-disk hooks: `WorktreeSetup`, `PrepareHost`,
  `GenerateArtifacts`, `ReservedArgs`, `DefaultCatalog`.

## Registry

`Options` is the operator-declared part of an adapter's configuration
(`RepoRoot`, `Command`, `ExtraArgs`, `Env`). `Register` adds a factory,
panicking on a duplicate name or a nil factory; `New` builds one by name;
`Names` lists them.

## Configuration

`BackendConfig` is a `shem.yaml` `backend:` block: the adapter name, how to
invoke it, and an inline `models.Catalog`. `LoadBackendConfig` reads the block
from under a document's one `backend:` key, rejecting unknown keys;
`MarshalDocument` and `Write` emit it again. `ResolveBackend` fills the catalog
from the adapter's `DefaultCatalog`: a block that declares `models` is used as
written, otherwise only the catalog keys it declares override the default.
`Validate` rejects an unknown adapter, an `extra_args` token the adapter sets
itself, an `env` name golem owns, and a structurally bad catalog. `NewAdapter` and
`Options` build the adapter.

## Process construction

`agentCmd` builds every agent subprocess: `agentenv.Environ` plus the block's
own `env` minus golem's variables, `PWD` set to the working directory
(opencode trusts it over getcwd), privileges dropped and pipe waits bounded.
`skipWorktree` keeps golem's edits to a tracked config file out of
`git add -A`.

## ClaudeCode

Runs the `claude` CLI, authenticated on the host, storing no credentials. Its
`argv` refuses a model id that is not a safe argv token rather than dropping
the flag. `PrepareHost` pre-accepts the workspace trust dialog for a repository
(`claudecode_trust.go`); `GenerateArtifacts` writes the subagent definitions
and commands. `DefaultCatalog` is `claudecode_catalog.yaml`.

## Codex

Runs `codex exec --skip-git-repo-check --sandbox <mode> [--model m] -`, the
prompt on stdin. The mode is `workspace-write` unless `GOLEM_CODEX_SANDBOX`
names another (the container sets `danger-full-access`: codex's bwrap sandbox
cannot start under Docker). `GenerateArtifacts` and `WorktreeSetup` write
`.codex/rules/golem.rules`, the claude allow/deny list as prefix rules;
`PrepareHost` marks the repo trusted in `~/.codex/config.toml` for root and
the agent account (`codex_trust.go`). `DefaultCatalog` is
`codex_catalog.yaml`.

## OpenCode

Runs `opencode run [--model provider/model]`, the prompt on stdin.
`WorktreeSetup` and `GenerateArtifacts` merge golem's `permission` block into
`opencode.json`, keeping the operator's keys, rules and key order; bash is a
`deny` catch-all, golem's allows, the operator's entries, then golem's denies
(last match wins). Never `ask`: `opencode run` aborts on one. A file
`encoding/json` cannot read (JSONC) is left alone. `GenerateArtifacts` also
projects roles into `.opencode/agents/`. `PrepareHost` is a no-op.
`DefaultCatalog` is `opencode_catalog.yaml`.
