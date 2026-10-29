# Shem safety, setup and prerequisites

Everything here is a guardrail against accidents, not a sandbox: a shell command
can still write outside the worktree or reach the network in ways the checks do
not recognize. Use OS filesystem and network isolation when that must hold.

## Prerequisites

- Git 2.40+ with `git check-attr --source` (checked at ticket creation and shem
  startup).
- Go 1.27+ and a C compiler to build from source.
- A backend CLI with hook support: Claude Code needs `--settings`, `--tools` and
  `--strict-mcp-config`; Codex needs PreToolUse hooks; OpenCode
  needs local plugins (`node`) and `tool.execute.before`.
- A POSIX shell (`sh`) for setup commands and hooks; on Windows use Git Bash or
  WSL. Keep `golem` on PATH for the hooks.

## Secret-free checkout

Give a shem a dedicated clone with synthetic fixtures and `.env.example`
templates, never a production directory. The worker refuses a checkout holding
live `.env`, `.envrc`, `.netrc`, `.npmrc`, `.git-credentials`, ssh keys, `*.pem`
or `*.key` files, before the agent gets the checkout and after bootstrap.
`.git`, `.golem`, `node_modules`, `.venv`, `venv`, `vendor`, `target`, `testdata`
and `__pycache__` are not scanned; `.env.example`, `.env.sample` and
`.env.template` are allowed. Agent processes still get the provider credentials
their backend needs; golem's own `GOLEM_*` credentials are filtered out.

## Tool policy

Every adapter installs a per-invocation hook outside the checkout (Claude
settings hook, Codex inline hook, OpenCode plugin) that runs `golem policy-check`
for each tool call; the same Go policy also produces the native command denies.
An installation error fails the invocation.

- `allow_worktree_only` defaults to true; set `tool_policy.allow_worktree_only:
  false` to turn it off. It scopes file-write tools to the ticket worktree (the
  ticket directory in document phases).
- `tool_policy.allow_network` lists the hosts curl/wget URLs and `WebFetch` may
  reach.
- Live env/key paths are denied, including common copy/read commands.
- Recognized tools: shell (`Bash`, `exec_command`), writes (`Write`, `Edit`,
  `MultiEdit`, `NotebookEdit`, `apply_patch`, OpenCode `patch`), `WebFetch`, and
  read-only or self-gated tools (`Read`, `Glob`, `Grep`, `LS`, todo and plan
  tools, `Agent`/`Task`, `Skill`, `ToolSearch`, `BashOutput`, `KillShell`). A
  subagent's own calls pass through the same hook. `WebSearch` and every other
  tool are denied.
- A denied command (`git push`, `gh pr create`, `sudo`, ...) matches only where
  a command starts: line start, or after `;`, `&&`, `||`, `|`, `(`, a backtick,
  `$(` or `sh -c`. `git commit -m "drop sudo"`, `source` and `eval` are allowed.

The diagnoser runs in a private directory with no tools: Claude disables
built-in and MCP tools, OpenCode denies all tools, and Codex runs read-only with a hook that
denies every tool.

Evidence is redacted before phase logs, blackboard logs, failure uploads and
model prompts: sensitive-file diff sections, process-secret values, credential
assignments and private-key blocks. It cannot recognize every secret.

Limits: Claude project settings cannot switch the hook off (`disableAllHooks` is
false in the generated settings). The Codex hook trust format was checked against codex 0.163
(PreToolUse runs and allows). A trust mismatch may leave the hook silently unloaded, and `--strict-config`
is not used (it rejects newer host configs and does not validate the override), so re-check a denied
call after upgrading Codex. An unparseable `opencode.json` is logged and left alone.

## Project bootstrap

Add commands to the repository's entry in `shem.yaml`, not to a ticket:

```yaml
repos:
  - path: /repos/project
    remote: https://example.com/team/project.git
    worktree_setup:
      - npm ci
    setup_timeout: 5m
```

Commands run in the prepared worktree before agent work, with the filtered
environment and the agent account; the timeout covers all of them. Success is
recorded in the worktree's git directory (`golem-worktree-setup`), so retries
and ordinary commits skip setup; a recreated worktree, changed commands or a
different pinned base runs it again. Delete that file to rerun deliberately.

## Environment failures

A missing tool, failed setup, live secret file or unsupported Git starts with
`GOLEM_ENVIRONMENT_FAILURE:`; automatic repair stops and the ticket waits in
needs-attention. Put host prerequisites in `gate.environment_checks` in
`.golem/config.yaml` (they run before `gate.commands`); a failing check, exit 127
or exit 126 is an environment failure, while other failing commands stay
repairable.
