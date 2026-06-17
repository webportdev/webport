#!/usr/bin/env bash
#
# register-route.sh - Register a route with webport
#
# Environment Variables:
#   WEBPORT_PROJECT      Project name (required, or uses git repo name)
#   WEBPORT_BRANCH       Git branch name (required, or uses current git branch)
#   APP_PORT             Local port to proxy to (required)
#   WEBPORT_TTL          TTL in seconds (optional, default: 300)
#   WEBPORT_API_PORT     Webport API port (optional, default: 8080)
#
# Usage:
#   # Set environment variables
#   export WEBPORT_PROJECT="myapp"
#   export WEBPORT_BRANCH="main"
#   export APP_PORT=3000
#
#   # Or let it auto-detect from git
#   cd /path/to/git/repo
#   export APP_PORT=3000
#
#   ./scripts/register-route.sh
#

set -euo pipefail

# Configuration from environment with defaults
WEBPORT_API_PORT="${WEBPORT_API_PORT:-8080}"
WEBPORT_API_URL="${WEBPORT_API_URL:-http://localhost:${WEBPORT_API_PORT}}"
WEBPORT_TTL="${WEBPORT_TTL:-300}"

# Auto-detect project name from git repo basename if not set
if [[ -z "${WEBPORT_PROJECT:-}" ]]; then
  GIT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || true)
  if [[ -n "$GIT_ROOT" ]]; then
    WEBPORT_PROJECT=$(basename "$GIT_ROOT")
    echo "Auto-detected project: $WEBPORT_PROJECT"
  else
    echo "Error: WEBPORT_PROJECT not set and could not detect from git repository" >&2
    echo "Set WEBPORT_PROJECT environment variable or run from within a git repository" >&2
    exit 1
  fi
fi

# Auto-detect branch name from git if not set
if [[ -z "${WEBPORT_BRANCH:-}" ]]; then
  WEBPORT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
  echo "Auto-detected branch: $WEBPORT_BRANCH"
fi

# Validate required variables
if [[ -z "${APP_PORT:-}" ]]; then
  echo "Error: APP_PORT is required" >&2
  echo "Usage: APP_PORT=3000 $0" >&2
  exit 1
fi

# Ensure port is a number
if ! [[ "$APP_PORT" =~ ^[0-9]+$ ]]; then
  echo "Error: APP_PORT must be a number, got: $APP_PORT" >&2
  exit 1
fi

# Sanitize branch name - replace slashes with dashes for route ID
BRANCH_SANITIZED=$(echo "$WEBPORT_BRANCH" | sed 's/\//-/g')

# Build the JSON payload
JSON_PAYLOAD=$(cat <<EOF
{
  "project": "${WEBPORT_PROJECT}",
  "branch": "${WEBPORT_BRANCH}",
  "port": ${APP_PORT},
  "ttl": ${WEBPORT_TTL}
}
EOF
)

echo "Registering route with webport..."
echo "  Project: ${WEBPORT_PROJECT}"
echo "  Branch: ${WEBPORT_BRANCH}"
echo "  Port: ${APP_PORT}"
echo "  TTL: ${WEBPORT_TTL}s"
echo "  API URL: ${WEBPORT_API_URL}"
echo ""

# Make the API call
 RESPONSE=$(curl -s -w "\n%{http_code}" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$JSON_PAYLOAD" \
  "${WEBPORT_API_URL}/routes")

# Split response and status code
HTTP_STATUS=$(echo "$RESPONSE" | tail -n1)
HTTP_BODY=$(echo "$RESPONSE" | head -n-1)

if [[ "$HTTP_STATUS" == "200" ]] || [[ "$HTTP_STATUS" == "201" ]]; then
  echo "Route registered successfully!"

  # Try to extract the domain from response
  DOMAIN=$(echo "$HTTP_BODY" | grep -o '"domain":"[^"]*"' | cut -d'"' -f4)
  if [[ -n "$DOMAIN" ]]; then
    echo "  URL: https://${DOMAIN}"
  fi

  # Pretty print the full response if jq is available
  if command -v jq &>/dev/null; then
    echo ""
    echo "$HTTP_BODY" | jq .
  fi
else
  echo "Error: Failed to register route (HTTP ${HTTP_STATUS})" >&2
  echo "$HTTP_BODY" >&2
  exit 1
fi
