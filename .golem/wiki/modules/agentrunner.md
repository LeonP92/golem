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
own `env` minus golem's variables, with privileges dropped and pipe waits
bounded.

## ClaudeCode

Runs the `claude` CLI, authenticated on the host, storing no credentials. Its
`argv` refuses a model id that is not a safe argv token rather than dropping
the flag. `PrepareHost` pre-accepts the workspace trust dialog for a repository
(`claudecode_trust.go`); `GenerateArtifacts` writes the subagent definitions
and commands. `DefaultCatalog` is `claudecode_catalog.yaml`.
