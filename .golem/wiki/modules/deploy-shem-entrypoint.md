# deploy/shem-entrypoint.sh

Docker Compose entrypoint for the `shem` service. Bootstraps the Claude Code
CLI's config before handing off to `golem-shem`.

## What it does

Supports three mutually-compatible Claude Code auth modes, wired through
`.env.example`, `docker-compose.yml`, `run.sh`, and `Dockerfile` comments:

- **`CLAUDE_HOME`** — mounts the host's `~/.claude` session directory; the
  entrypoint restores `.claude.json` from the newest backup if missing.
- **`CLAUDE_CODE_OAUTH_TOKEN`** — long-lived token from `claude setup-token`
  for a Pro/Max subscription, used headlessly.
- **`ANTHROPIC_API_KEY`** — pay-as-you-go API key, used headlessly.

For the two headless vars, if no `~/.claude.json` exists yet the entrypoint
writes a minimal `{"hasCompletedSetup":true,"projects":{}}` so Claude Code
skips its interactive first-run wizard. All three vars simply reach the
`claude` subprocess via inherited environment — no Go code is involved;
`internal/agentrunner` and `internal/shem/worker` spawn `claude` without
overriding `cmd.Env`.

`run.sh`'s startup validation passes if any of the three vars is set.

## Why it exists

Lets shems run headlessly (CI, remote nodes, containers without host
`~/.claude` access) using either a subscription-backed OAuth token or a
metered API key, without requiring the interactive Claude Code login flow.
