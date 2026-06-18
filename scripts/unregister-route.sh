#!/usr/bin/env bash
#
# unregister-route.sh - Remove a route from webport
#
# Environment Variables:
#   WEBPORT_PROJECT      Project name (required, or uses git repo name)
#   WEBPORT_BRANCH       Git branch name (required, or uses current git branch)
#   WEBPORT_API_PORT     Webport API port (optional, default: 8080)
#
# Usage:
#   export WEBPORT_PROJECT="myapp"
#   export WEBPORT_BRANCH="main"
#   ./scripts/unregister-route.sh
#

set -euo pipefail

# Configuration from environment with defaults
WEBPORT_API_PORT="${WEBPORT_API_PORT:-8080}"
WEBPORT_API_URL="${WEBPORT_API_URL:-http://localhost:${WEBPORT_API_PORT}}"

# Auto-detect project name from git repo basename if not set
if [[ -z "${WEBPORT_PROJECT:-}" ]]; then
  GIT_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || true)
  if [[ -n "$GIT_ROOT" ]]; then
    WEBPORT_PROJECT=$(basename "$GIT_ROOT")
  else
    echo "Error: WEBPORT_PROJECT not set and could not detect from git repository" >&2
    exit 1
  fi
fi

# Auto-detect branch name from git if not set
if [[ -z "${WEBPORT_BRANCH:-}" ]]; then
  WEBPORT_BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
fi

# Build route ID (project:branch). Slashes must be escaped inside the URL path segment.
BRANCH_ESCAPED=${WEBPORT_BRANCH//\//%2F}
ROUTE_ID="${WEBPORT_PROJECT}:${BRANCH_ESCAPED}"

echo "Removing route: ${ROUTE_ID}"

HTTP_STATUS=$(curl -s -w "%{http_code}" \
  -X DELETE \
  "${WEBPORT_API_URL}/routes/${ROUTE_ID}")

if [[ "$HTTP_STATUS" == "204" ]] || [[ "$HTTP_STATUS" == "200" ]]; then
  echo "Route removed successfully!"
else
  echo "Error: Failed to remove route (HTTP ${HTTP_STATUS})" >&2
  exit 1
fi
