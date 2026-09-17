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

# Validate required vars.
if [ -z "$ANTHROPIC_API_KEY" ] && [ -z "$CLAUDE_HOME" ] && [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ]; then
  echo "Error: set one of CLAUDE_HOME, CLAUDE_CODE_OAUTH_TOKEN, or ANTHROPIC_API_KEY in .env" >&2
  exit 1
fi
: "${GOLEM_ADMIN_PASSWORD:?Set GOLEM_ADMIN_PASSWORD in .env}"
: "${GOLEM_SHEM_API_KEY:?Set GOLEM_SHEM_API_KEY in .env}"

# Generate a random shem key if still using the placeholder.
if [ "$GOLEM_SHEM_API_KEY" = "changeme-replace-with-a-long-random-string" ]; then
  GOLEM_SHEM_API_KEY=$(openssl rand -hex 32)
  sed -i "s|^GOLEM_SHEM_API_KEY=.*|GOLEM_SHEM_API_KEY=$GOLEM_SHEM_API_KEY|" .env
  echo "Generated GOLEM_SHEM_API_KEY and saved to .env"
fi

export GOLEM_SHEM_API_KEY

# Convert a Windows path (C:/Users/...) to the /c/Users/... format Docker expects.
if [ -n "$CLAUDE_HOME" ]; then
  case "$CLAUDE_HOME" in
    [A-Za-z]:*)
      drive=$(echo "$CLAUDE_HOME" | cut -c1 | tr 'A-Z' 'a-z')
      rest=$(echo "$CLAUDE_HOME" | cut -c3- | sed 's|\\|/|g; s|^/||')
      CLAUDE_HOME="/$drive/$rest"
      export CLAUDE_HOME
      ;;
  esac

  # Checked here because Docker's own message for a missing bind-mount source
  # is actively misleading: it reports the path as "not shared from the host"
  # and points at Preferences -> Resources -> File Sharing, which diagnoses a
  # permissions problem when the real one is that the directory does not
  # exist. That sends people to configure sharing for a path they never meant
  # to mount.
  if [ ! -d "$CLAUDE_HOME" ]; then
    echo "Error: CLAUDE_HOME points at $CLAUDE_HOME, which is not a directory." >&2
    if [ "$CLAUDE_HOME" != "${CLAUDE_HOME#/c/Users/YourName}" ]; then
      echo "       That is the placeholder from .env.example. Set it to your own" >&2
      echo "       ~/.claude directory, or comment it out and use" >&2
      echo "       CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY instead." >&2
    else
      echo "       Set it to your ~/.claude directory, or comment it out to use a" >&2
      echo "       throwaway volume with CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY." >&2
    fi
    exit 1
  fi
fi

docker compose up -d

echo ""
echo "Golem is running."
echo "  Dashboard: http://localhost:8080"
echo "  Username:  ${GOLEM_ADMIN_USERNAME:-admin}"
echo "  Password:  ${GOLEM_ADMIN_PASSWORD}"
