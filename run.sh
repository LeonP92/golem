#!/usr/bin/env sh
# Golem local quick-start. Sets up .env if missing, then starts the stack.
set -e

# Generate .env from example if it doesn't exist.
if [ ! -f .env ]; then
  cp .env.example .env
  echo "Created .env from .env.example — edit it before re-running."
  exit 1
fi

# Load .env
set -a
. ./.env
set +a

# Validate required vars. The credential check follows the selected backend;
# claude-code is the default and matches the stack's original behaviour.
GOLEM_BACKEND="${GOLEM_BACKEND:-claude-code}"
case "$GOLEM_BACKEND" in
  claude-code)
    if [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$CLAUDE_HOME" ] && [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ]; then
      echo "Error: set one of CLAUDE_HOME, CLAUDE_CODE_OAUTH_TOKEN, or ANTHROPIC_API_KEY in .env" >&2
      exit 1
    fi
    ;;
  codex)
    if [ -z "$CODEX_API_KEY" ] && [ -z "$OPENAI_API_KEY" ] && [ -z "$CODEX_HOME" ]; then
      echo "Error: set one of CODEX_HOME, CODEX_API_KEY, or OPENAI_API_KEY in .env" >&2
      exit 1
    fi
    ;;
  opencode)
    if [ -z "$OPENCODE_HOME" ] && [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$OPENAI_API_KEY" ]; then
      echo "Error: set OPENCODE_HOME or a provider key (ANTHROPIC_API_KEY, OPENAI_API_KEY) in .env" >&2
      exit 1
    fi
    ;;
  *)
    echo "Error: unknown GOLEM_BACKEND '$GOLEM_BACKEND' (known: claude-code, codex, opencode)" >&2
    exit 1
    ;;
esac
: "${GOLEM_ADMIN_PASSWORD:?Set GOLEM_ADMIN_PASSWORD in .env}"

# Length is checked here, not just by the orchestrator, because the failure is
# otherwise invisible: the container logs a warning, starts anyway, and the
# only symptom is a login page answering "Invalid username or password" for
# credentials the operator can see sitting in their own .env.
if [ "${#GOLEM_ADMIN_PASSWORD}" -lt 8 ]; then
  echo "Error: GOLEM_ADMIN_PASSWORD is ${#GOLEM_ADMIN_PASSWORD} characters; the minimum is 8." >&2
  echo "       Shorter passwords are rejected when the admin account is created," >&2
  echo "       which leaves the dashboard with no account to sign in to." >&2
  exit 1
fi
: "${GOLEM_SHEM_API_KEY:?Set GOLEM_SHEM_API_KEY in .env}"

# Generate a random shem key if still using the placeholder.
if [ "$GOLEM_SHEM_API_KEY" = "changeme-replace-with-a-long-random-string" ]; then
  GOLEM_SHEM_API_KEY=$(openssl rand -hex 32)
  sed -i "s|^GOLEM_SHEM_API_KEY=.*|GOLEM_SHEM_API_KEY=$GOLEM_SHEM_API_KEY|" .env
  echo "Generated GOLEM_SHEM_API_KEY and saved to .env"
fi

export GOLEM_SHEM_API_KEY

# Convert a Windows path (C:/Users/...) to the /c/Users/... format Docker expects,
# for each backend session mount that is set.
for home_var in CLAUDE_HOME CODEX_HOME OPENCODE_HOME; do
  eval "home_path=\$$home_var"
  if [ -n "$home_path" ]; then
    case "$home_path" in
      [A-Za-z]:*)
        drive=$(echo "$home_path" | cut -c1 | tr 'A-Z' 'a-z')
        rest=$(echo "$home_path" | cut -c3- | sed 's|\\|/|g; s|^/||')
        home_path="/$drive/$rest"
        eval "$home_var=\"$home_path\""
        eval "export $home_var"
        ;;
    esac

    # Checked here because Docker's own message for a missing bind-mount source
    # is actively misleading: it reports the path as "not shared from the host"
    # and points at Preferences -> Resources -> File Sharing, which diagnoses a
    # permissions problem when the real one is that the directory does not
    # exist. That sends people to configure sharing for a path they never meant
    # to mount.
    if [ ! -d "$home_path" ]; then
      echo "Error: $home_var points at $home_path, which is not a directory." >&2
      if [ "$home_path" != "${home_path#/c/Users/YourName}" ]; then
        echo "       That is the placeholder from .env.example. Set it to your own" >&2
        echo "       session directory, or comment it out and use an API key instead." >&2
      else
        echo "       Set it to your session directory, or comment it out to use a" >&2
        echo "       throwaway volume with an API key instead." >&2
      fi
      exit 1
    fi
  fi
done

docker compose up -d

echo ""
echo "Golem is running."
echo "  Dashboard: http://localhost:8080"
echo "  Username:  ${GOLEM_ADMIN_USERNAME:-admin}"
echo "  Password:  ${GOLEM_ADMIN_PASSWORD}"
