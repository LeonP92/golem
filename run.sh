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
: "${ANTHROPIC_API_KEY:?Set ANTHROPIC_API_KEY in .env}"
: "${GOLEM_ADMIN_PASSWORD:?Set GOLEM_ADMIN_PASSWORD in .env}"
: "${GOLEM_SHEM_API_KEY:?Set GOLEM_SHEM_API_KEY in .env}"

# Generate a random shem key if still using the placeholder.
if [ "$GOLEM_SHEM_API_KEY" = "changeme-replace-with-a-long-random-string" ]; then
  GOLEM_SHEM_API_KEY=$(openssl rand -hex 32)
  sed -i "s|^GOLEM_SHEM_API_KEY=.*|GOLEM_SHEM_API_KEY=$GOLEM_SHEM_API_KEY|" .env
  echo "Generated GOLEM_SHEM_API_KEY and saved to .env"
fi

export GOLEM_SHEM_API_KEY

docker compose up -d

echo ""
echo "Golem is running."
echo "  Dashboard: http://localhost:8080"
echo "  Username:  ${GOLEM_ADMIN_USERNAME:-admin}"
echo "  Password:  ${GOLEM_ADMIN_PASSWORD}"
