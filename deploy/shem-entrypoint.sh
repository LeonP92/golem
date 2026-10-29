#!/bin/sh
# Shem container entrypoint.
# Prepares the agent backend's environment before starting golem-shem.
#
# GOLEM_BACKEND (required, no default) selects which backend block below
# runs; the image must have been built with that backend in GOLEM_BACKENDS
# (see Dockerfile) for its CLI to exist. The published release images install
# all three CLIs, so the variable alone picks the backend at runtime.
#
# Everything this container needs arrives through the environment, and a
# missing or unknown value stops the container here, at startup — not in a
# warning nobody reads, and not as a ticket that fails its first phase.
#
# claude-code auth modes (auto-detected — no manual configuration required):
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
# codex and opencode auth: see .env.example. Host session dirs are mounted
# read-only at /mnt/codex-home and /mnt/opencode-home; only their
# credential files are copied in.
set -e

# No default: the backend is runtime configuration, and guessing it used to
# start a claude-code shem for an operator who never asked for one.
if [ -z "$GOLEM_BACKEND" ]; then
  echo "shem: GOLEM_BACKEND is not set — name the backend this shem runs:" >&2
  echo "shem:   claude-code, codex or opencode. Set it in .env; see .env.example." >&2
  exit 1
fi
case "$GOLEM_BACKEND" in
  claude-code) golem_backend_cli=claude ;;
  codex)       golem_backend_cli=codex ;;
  opencode)    golem_backend_cli=opencode ;;
  *)
    echo "shem: unknown GOLEM_BACKEND '$GOLEM_BACKEND' (known: claude-code, codex, opencode)." >&2
    exit 1
    ;;
esac

# A backend whose CLI is not in the image fails its first phase, minutes after
# startup and one ticket deep. Name the mismatch now: the image was built
# without GOLEM_BACKENDS including this backend.
if ! command -v "$golem_backend_cli" >/dev/null 2>&1; then
  echo "shem: GOLEM_BACKEND=$GOLEM_BACKEND but its CLI ('$golem_backend_cli') is not in this image." >&2
  echo "shem: rebuild with $GOLEM_BACKEND in GOLEM_BACKENDS (see Dockerfile), or use an" >&2
  echo "shem: image that ships it." >&2
  exit 1
fi

CLAUDE_JSON="${HOME}/.claude.json"
CLAUDE_DIR="${HOME}/.claude"
CODEX_DIR="${HOME}/.codex"
OPENCODE_DIR="${HOME}/.local/share/opencode"

# --- claude-code: auth --------------------------------------------------------
if [ "$GOLEM_BACKEND" = "claude-code" ]; then
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
fi # GOLEM_BACKEND = claude-code

# --- codex / opencode: auth -----------------------------------------------------
# The host session is mounted read-only (docker-compose.yml) and found by its
# files, not by CODEX_HOME / OPENCODE_HOME, which are host paths and are not
# passed in. Only the credential files are copied, so nothing golem writes
# later (trust entries) reaches the host.
CODEX_MOUNT="${GOLEM_CODEX_MOUNT:-/mnt/codex-home}"
OPENCODE_MOUNT="${GOLEM_OPENCODE_MOUNT:-/mnt/opencode-home}"
copy_from_mount() { # <src dir> <dest dir> <file>...
  src=$1; dest=$2; shift 2
  for f in "$@"; do
    [ -f "$src/$f" ] || continue
    mkdir -p "$dest"
    cp -f "$src/$f" "$dest/$f" || echo "shem: WARNING could not copy $src/$f to $dest" >&2
    if [ "$f" = auth.json ]; then chmod 600 "$dest/$f" 2>/dev/null || true; fi
  done
}
if [ "$GOLEM_BACKEND" = "codex" ]; then
  copy_from_mount "$CODEX_MOUNT" "$CODEX_DIR" auth.json config.toml
fi
if [ "$GOLEM_BACKEND" = "opencode" ]; then
  copy_from_mount "$OPENCODE_MOUNT" "$OPENCODE_DIR" auth.json
fi

# --- Credentials: required, checked at startup --------------------------------
# The agent cannot answer a login prompt, so a missing credential is not a
# degraded mode to start in — every phase would fail on it. Stop here instead.
case "$GOLEM_BACKEND" in
claude-code)
  if [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ] &&
     [ ! -f "$CLAUDE_DIR/.credentials.json" ]; then
    echo "shem: no claude-code credentials — the agent would report \"Not logged in\" on" >&2
    echo "shem:   every phase. Mount a logged-in CLAUDE_HOME, or set ANTHROPIC_API_KEY" >&2
    echo "shem:   or CLAUDE_CODE_OAUTH_TOKEN." >&2
    exit 1
  fi
  ;;
codex)
  if [ ! -f "$CODEX_DIR/auth.json" ] && [ -z "$CODEX_API_KEY" ] &&
     [ -z "$CODEX_ACCESS_TOKEN" ] && [ -z "$OPENAI_API_KEY" ]; then
    echo "shem: no codex credentials — every phase would fail. Mount a logged-in" >&2
    echo "shem:   CODEX_HOME, or set CODEX_API_KEY, CODEX_ACCESS_TOKEN or OPENAI_API_KEY." >&2
    exit 1
  fi
  ;;
opencode)
  if [ ! -f "$OPENCODE_DIR/auth.json" ] && [ -z "$ANTHROPIC_API_KEY" ] &&
     [ -z "$OPENAI_API_KEY" ] && [ -z "$OPENCODE_CONFIG_CONTENT" ]; then
    echo "shem: no opencode credentials — every phase would fail. Mount a logged-in" >&2
    echo "shem:   OPENCODE_HOME, or set ANTHROPIC_API_KEY, OPENAI_API_KEY or" >&2
    echo "shem:   OPENCODE_CONFIG_CONTENT." >&2
    exit 1
  fi
  ;;
esac

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
    case "$GOLEM_BACKEND" in
    claude-code)
      mkdir -p "$agent_home/.claude"
      # Session mode: the agent cannot read root's home, so give it a copy of
      # the mounted session. Gated on the mounted DIRECTORY, not on
      # .claude.json — that file lives beside ~/.claude, not inside it, so a
      # host whose mount carries only the directory still has credentials to
      # copy. Failures are reported: a silent one leaves the agent with a
      # config that looks fine and no credentials in it.
      if [ -n "$CLAUDE_HOME" ] && [ -d "$CLAUDE_DIR" ]; then
        if ! cp -R "$CLAUDE_DIR/." "$agent_home/.claude/"; then
          echo "shem: WARNING could not copy $CLAUDE_DIR into $agent_home/.claude" >&2
        fi
        if [ -f "$CLAUDE_JSON" ] && ! cp -f "$CLAUDE_JSON" "$agent_home/.claude.json"; then
          echo "shem: WARNING could not copy $CLAUDE_JSON to $agent_home" >&2
        fi
      fi
      if [ ! -f "$agent_home/.claude.json" ]; then
        printf '{"hasCompletedSetup":true,"projects":{}}\n' > "$agent_home/.claude.json"
      fi
      ;;
    codex)
      copy_from_mount "$CODEX_DIR" "$agent_home/.codex" auth.json config.toml
      ;;
    opencode)
      copy_from_mount "$OPENCODE_DIR" "$agent_home/.local/share/opencode" auth.json
      ;;
    esac
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
# GOLEM_PUSH_TOKEN is the general form: the shem merges epic children into
# their run's integration branch itself, on whatever git remote the run is
# on, so the helper is not scoped to one host. GOLEM_GITHUB_TOKEN remains
# for deployments that only ever push to github.com, and stays scoped there
# so the token cannot be offered to any other host.
#
# The helper reads the variable from the environment at use time instead of
# baking it into ~/.gitconfig, so the token is never written to disk and
# never appears in a `git config --list` dump.
#
# Doing nothing when both variables are empty is the supported default: with
# `no_push: true` in shem.yaml (the shipped setting) there is no push at all,
# and a host that already has SSH keys or a credential manager keeps using
# them.
if [ -n "$GOLEM_PUSH_TOKEN" ]; then
  git config --global credential.helper \
    '!f() { echo username=x-access-token; echo "password=$GOLEM_PUSH_TOKEN"; }; f'
  echo "shem: configured push credential from GOLEM_PUSH_TOKEN"
elif [ -n "$GOLEM_GITHUB_TOKEN" ]; then
  git config --global credential."https://github.com".helper \
    '!f() { echo username=x-access-token; echo "password=$GOLEM_GITHUB_TOKEN"; }; f'
  echo "shem: configured github.com push credential from GOLEM_GITHUB_TOKEN"
fi

# --- Workspace trust (claude-code) --------------------------------------------
if [ "$GOLEM_BACKEND" = "claude-code" ]; then
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
fi # GOLEM_BACKEND = claude-code

# --- Project trust (codex) ------------------------------------------------------
# Codex loads a project's .codex/ layer — including the rules file golem
# writes — only when the project is trusted, and `codex exec` cannot answer
# the trust prompt. Mark every repo under /repos/ trusted in both root's and
# the agent's ~/.codex/config.toml, the codex equivalent of the claude
# workspace-trust block above. Repositories cloned later are covered by the
# codex adapter's PrepareHost.
if [ "$GOLEM_BACKEND" = "codex" ]; then
  trust_configs="$CODEX_DIR/config.toml"
  if [ -n "$agent_home" ]; then
    trust_configs="$trust_configs $agent_home/.codex/config.toml"
  fi
  repos=$(find /repos -mindepth 1 -maxdepth 1 -type d 2>/dev/null || true)
  for trust_toml in $trust_configs; do
    mkdir -p "$(dirname "$trust_toml")"
    touch "$trust_toml"
    for repo in $repos; do
      if ! grep -qF "\"$repo\"" "$trust_toml"; then
        printf '\n[projects."%s"]\ntrust_level = "trusted"\n' "$repo" >> "$trust_toml"
        echo "shem: trusted codex project $repo"
      fi
    done
  done
fi

# python3 above ran as root; hand the agent back its own files.
if [ -n "$GOLEM_AGENT_USER" ] && [ -n "$agent_home" ]; then
  chown -R "$GOLEM_AGENT_USER" "$agent_home"
fi

exec golem-shem "$@"
