#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
WEBPORT_INSTALL_PLATFORM=darwin exec "$SCRIPT_DIR/install.sh" "$@"
