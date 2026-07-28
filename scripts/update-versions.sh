#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=versions.env
source "$SCRIPT_DIR/versions.env"

APPLY=0
[[ "${1:-}" == --apply ]] && APPLY=1
[[ $# -le $APPLY ]] || { printf 'usage: %s [--apply]\n' "$0" >&2; exit 2; }

latest=$(curl -fsSL https://api.github.com/repos/traefik/traefik/releases/latest |
	sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
[[ -n "$latest" ]] || { printf 'error: could not resolve latest Traefik release\n' >&2; exit 1; }

printf 'TRAEFIK_VERSION current=%s latest=%s\n' "$TRAEFIK_VERSION" "$latest"
(( APPLY )) || exit 0

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
base="https://github.com/traefik/traefik/releases/download/$latest"
curl -fsSL -o "$tmp/checksums.txt" "$base/traefik_${latest}_checksums.txt"
checksum() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}
for os in linux darwin; do
	for arch in amd64 arm64; do
		asset="traefik_${latest}_${os}_${arch}.tar.gz"
		curl -fsSL -o "$tmp/$asset" "$base/$asset"
		expected=$(grep -E "[[:space:]]\\*?$asset$" "$tmp/checksums.txt" | awk '{print $1}')
		actual=$(checksum "$tmp/$asset")
		[[ -n "$expected" && "$actual" == "$expected" ]] || {
			printf 'error: checksum validation failed for %s\n' "$asset" >&2
			exit 1
		}
	done
done

sed "s/^TRAEFIK_VERSION=.*/TRAEFIK_VERSION=$latest/" "$SCRIPT_DIR/versions.env" >"$tmp/versions.env"
mv "$tmp/versions.env" "$SCRIPT_DIR/versions.env"
printf 'updated Traefik pin to %s\n' "$latest"
