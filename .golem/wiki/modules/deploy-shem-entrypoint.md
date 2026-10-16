# deploy/shem-entrypoint.sh

Entrypoint for the compose `shem` service: prepares the backend named by
`GOLEM_BACKEND` (default `claude-code`), then execs `golem-shem`. Auth options
are documented once, in `.env.example`.

## What it does

- **claude-code**: restores `.claude.json` from a backup when `CLAUDE_HOME` is
  mounted, or bootstraps a minimal one for `CLAUDE_CODE_OAUTH_TOKEN` /
  `ANTHROPIC_API_KEY`; trusts every `/repos/*` in `.claude.json`.
- **codex**: copies `auth.json` and `config.toml` from the read-only mount at
  `/mnt/codex-home` (`GOLEM_CODEX_MOUNT` in tests); warns when there is no
  login, `CODEX_API_KEY`, `CODEX_ACCESS_TOKEN` or `OPENAI_API_KEY`; trusts
  every `/repos/*` in the container's `~/.codex/config.toml`, never the host's.
- **opencode**: copies `auth.json` from `/mnt/opencode-home`
  (`GOLEM_OPENCODE_MOUNT`); warns when there is no login, provider key or
  `OPENCODE_CONFIG_CONTENT`.
- Copies the prepared files into the agent account's home (`GOLEM_AGENT_USER`)
  and chowns them, since the agent runs unprivileged.
- Installs a github.com credential helper when `GOLEM_GITHUB_TOKEN` is set.

Session mounts are found by their files, not by `CODEX_HOME` /
`OPENCODE_HOME`: those are host paths and compose does not pass them in.
Tests: `deploy/entrypoint_*_test.go` run the script with `exec` stubbed out.
