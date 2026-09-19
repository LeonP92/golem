#!/bin/sh
# Shem container entrypoint.
# Prepares the Claude Code environment before starting golem-shem.
#
# Supported auth modes (auto-detected — no manual configuration required):
#
#   API key  — set ANTHROPIC_API_KEY in your .env file.
#              The entrypoint bootstraps a minimal ~/.claude.json so
#              Claude Code skips the interactive first-run wizard.
#
#   OAuth token — set CLAUDE_CODE_OAUTH_TOKEN in your .env file (generated
#              via `claude setup-token` for a Pro/Max subscription).
#              Same headless bootstrap as API key mode.
#
#   Session  — mount your host ~/.claude into the container via CLAUDE_HOME.
#              Claude Code reuses your existing desktop session.
#              Set CLAUDE_HOME to your host path in .env, e.g.:
#                CLAUDE_HOME=/home/you/.claude    (Linux/Mac)
#                CLAUDE_HOME=C:\Users\you\.claude (Windows)
#
# All modes pass --dangerously-skip-permissions at runtime so Claude Code
# can run tools unattended without per-call permission prompts.
set -e

CLAUDE_JSON="${HOME}/.claude.json"
CLAUDE_DIR="${HOME}/.claude"

# --- Auth mode: session (CLAUDE_HOME mounted) ---------------------------------
# Restore .claude.json from the latest backup if it's missing.
# This happens when CLAUDE_HOME is mounted but the .claude.json lives one
# level above the directory (at the user home root on the host machine).
#
# Gated on CLAUDE_HOME, which is what the paragraph above always meant but the
# condition never said. Without the gate, /root/.claude is the throwaway
# shem-claude volume, which keeps whatever Claude Code left there on an
# earlier run — so switching from CLAUDE_HOME to a headless token restored a
# STALE session on the next start, and because the bootstrap below only runs
# when .claude.json is absent, the token never got its clean config. The
# symptom is a container that has a perfectly good CLAUDE_CODE_OAUTH_TOKEN and
# still reports "Not logged in".
if [ -n "$CLAUDE_HOME" ] && [ ! -f "$CLAUDE_JSON" ]; then
  latest=$(ls -t "$CLAUDE_DIR/backups/.claude.json.backup."* 2>/dev/null | head -1)
  if [ -n "$latest" ]; then
    cp "$latest" "$CLAUDE_JSON"
    echo "shem: restored $CLAUDE_JSON from backup"
  fi
fi

# --- Auth mode: API key or OAuth token (no session) ---------------------------
# If either headless auth var is set and there is still no .claude.json,
# create a minimal one that tells Claude Code setup is done.  Without this,
# Claude Code shows an interactive first-run wizard that blocks headless
# execution.
if { [ -n "$ANTHROPIC_API_KEY" ] || [ -n "$CLAUDE_CODE_OAUTH_TOKEN" ]; } && [ ! -f "$CLAUDE_JSON" ]; then
  mkdir -p "$CLAUDE_DIR"
  printf '{"hasCompletedSetup":true,"projects":{}}\n' > "$CLAUDE_JSON"
  echo "shem: bootstrapped $CLAUDE_JSON for headless auth"
elif { [ -n "$ANTHROPIC_API_KEY" ] || [ -n "$CLAUDE_CODE_OAUTH_TOKEN" ]; } && [ -n "$CLAUDE_HOME" ]; then
  # Both auth modes configured at once. The bootstrap above is skipped
  # because CLAUDE_HOME brought a .claude.json with it, and Claude Code then
  # prefers the session in that file — which on a macOS host is a reference
  # to credentials kept in the Keychain and therefore absent here, giving
  #
  #   Failed to authenticate: OAuth session expired and could not be refreshed
  #
  # while the token sitting in the environment is never tried. Saying so is
  # the difference between a two-minute fix and a long afternoon.
  echo "shem: WARNING both CLAUDE_HOME and a headless auth variable are set." >&2
  echo "shem:   Claude Code will use the session from the mounted CLAUDE_HOME," >&2
  echo "shem:   not ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN. If that session" >&2
  echo "shem:   is expired or its credentials live in the host keychain (macOS)," >&2
  echo "shem:   comment out CLAUDE_HOME in .env and recreate this container." >&2
fi

# --- Agent account home -------------------------------------------------------
# The agent runs as GOLEM_AGENT_USER (see Dockerfile) so it cannot read
# GOLEM_GITHUB_TOKEN out of /proc/1/environ. That account has its own HOME,
# and Claude Code keeps its state under HOME, so the headless bootstrap above
# has to be repeated there: without it the agent gets the interactive
# first-run wizard and every phase hangs with no output.
if [ -n "$GOLEM_AGENT_USER" ]; then
  agent_home=$(awk -F: -v u="$GOLEM_AGENT_USER" '$1==u {print $6}' /etc/passwd)
  if [ -z "$agent_home" ]; then
    echo "shem: WARNING GOLEM_AGENT_USER=$GOLEM_AGENT_USER has no account; the agent will run as root" >&2
  else
    mkdir -p "$agent_home/.claude"
    if [ -n "$CLAUDE_HOME" ] && [ -f "$CLAUDE_JSON" ]; then
      # Session mode: the agent cannot read root's home, so give it a copy
      # of the mounted session rather than a bare bootstrap.
      cp -f "$CLAUDE_JSON" "$agent_home/.claude.json" 2>/dev/null || true
      cp -R "$CLAUDE_DIR/." "$agent_home/.claude/" 2>/dev/null || true
    elif [ ! -f "$agent_home/.claude.json" ]; then
      printf '{"hasCompletedSetup":true,"projects":{}}\n' > "$agent_home/.claude.json"
    fi
    chown -R "$GOLEM_AGENT_USER" "$agent_home"
    echo "shem: prepared $agent_home for agent account $GOLEM_AGENT_USER"
  fi
fi

# --- Git push credential ------------------------------------------------------
# The shem pushes the ticket branch that the orchestrator's pull request is
# opened from. That push happens on the shem host, so it needs its own
# credential — the orchestrator's token never reaches this container by any
# other route.
#
# The helper reads GOLEM_GITHUB_TOKEN from the environment at use time instead
# of baking it into ~/.gitconfig, so the token is never written to disk and
# never appears in a `git config --list` dump. Scoped to github.com so it
# cannot be offered to any other host.
#
# Doing nothing when the variable is empty is the supported default: with
# `no_push: true` in shem.yaml (the shipped setting) there is no push at all,
# and a host that already has SSH keys or a credential manager keeps using
# them.
if [ -n "$GOLEM_GITHUB_TOKEN" ]; then
  git config --global credential."https://github.com".helper \
    '!f() { echo username=x-access-token; echo "password=$GOLEM_GITHUB_TOKEN"; }; f'
  echo "shem: configured github.com push credential from GOLEM_GITHUB_TOKEN"
fi

# --- Workspace trust ----------------------------------------------------------
# Pre-accept the Claude Code workspace trust dialog for every repo the shem
# will work on.  Without this, Claude Code ignores the project's allow-list
# and warns on every invocation.
#
# Applied to the AGENT's config as well as root's, and the agent's is the one
# that actually matters: the agent runs as GOLEM_AGENT_USER with its own HOME,
# so it reads its own .claude.json. Trusting only root's left the agent
# hitting the trust dialog on a repo root it could not answer for, which in
# --print mode is a phase that produces nothing and never returns.
trust_configs="$CLAUDE_JSON"
if [ -n "$agent_home" ]; then
  trust_configs="$trust_configs $agent_home/.claude.json"
fi
for trust_json in $trust_configs; do
if [ -f "$trust_json" ] && command -v python3 > /dev/null 2>&1; then
  # Trust every repo directory mounted under /repos/ automatically.
  repos=$(find /repos -mindepth 1 -maxdepth 1 -type d 2>/dev/null | tr '\n' ' ')
  # shellcheck disable=SC2086
  python3 - "$trust_json" $repos <<'PYEOF'
import sys, json, os

path = sys.argv[1]
dirs = sys.argv[2:]

with open(path) as f:
    cfg = json.load(f)

projects = cfg.setdefault("projects", {})
changed = False
for d in dirs:
    if os.path.isdir(d):
        entry = projects.setdefault(d, {})
        if not entry.get("hasTrustDialogAccepted"):
            entry["hasTrustDialogAccepted"] = True
            changed = True
            print(f"shem: trusted project {d}")

if changed:
    with open(path, "w") as f:
        json.dump(cfg, f, indent=2)
PYEOF
fi
done

# python3 above ran as root; hand the agent back its own files.
if [ -n "$GOLEM_AGENT_USER" ] && [ -n "$agent_home" ]; then
  chown -R "$GOLEM_AGENT_USER" "$agent_home"
fi

exec golem-shem "$@"
