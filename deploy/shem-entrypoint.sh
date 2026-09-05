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
#   Session  — mount your host ~/.claude into the container via CLAUDE_HOME.
#              Claude Code reuses your existing desktop session.
#              Set CLAUDE_HOME to your host path in .env, e.g.:
#                CLAUDE_HOME=/home/you/.claude    (Linux/Mac)
#                CLAUDE_HOME=C:\Users\you\.claude (Windows)
#
# Both modes pass --dangerously-skip-permissions at runtime so Claude Code
# can run tools unattended without per-call permission prompts.
set -e

CLAUDE_JSON="${HOME}/.claude.json"
CLAUDE_DIR="${HOME}/.claude"

# --- Auth mode: session (CLAUDE_HOME mounted) ---------------------------------
# Restore .claude.json from the latest backup if it's missing.
# This happens when CLAUDE_HOME is mounted but the .claude.json lives one
# level above the directory (at the user home root on the host machine).
if [ ! -f "$CLAUDE_JSON" ]; then
  latest=$(ls -t "$CLAUDE_DIR/backups/.claude.json.backup."* 2>/dev/null | head -1)
  if [ -n "$latest" ]; then
    cp "$latest" "$CLAUDE_JSON"
    echo "shem: restored $CLAUDE_JSON from backup"
  fi
fi

# --- Auth mode: API key (ANTHROPIC_API_KEY set, no session) -------------------
# If the API key is set and there is still no .claude.json, create a minimal
# one that tells Claude Code setup is done.  Without this, Claude Code shows
# an interactive first-run wizard that blocks headless execution.
if [ -n "$ANTHROPIC_API_KEY" ] && [ ! -f "$CLAUDE_JSON" ]; then
  mkdir -p "$CLAUDE_DIR"
  printf '{"hasCompletedSetup":true,"projects":{}}\n' > "$CLAUDE_JSON"
  echo "shem: bootstrapped $CLAUDE_JSON for API key auth"
fi

# --- Workspace trust ----------------------------------------------------------
# Pre-accept the Claude Code workspace trust dialog for every repo the shem
# will work on.  Without this, Claude Code ignores the project's allow-list
# and warns on every invocation.
if [ -f "$CLAUDE_JSON" ] && command -v python3 > /dev/null 2>&1; then
  # Trust every repo directory mounted under /repos/ automatically.
  repos=$(find /repos -mindepth 1 -maxdepth 1 -type d 2>/dev/null | tr '\n' ' ')
  # shellcheck disable=SC2086
  python3 - "$CLAUDE_JSON" $repos <<'PYEOF'
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

exec golem-shem "$@"
