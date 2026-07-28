#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
INSTALLER="$SCRIPT_DIR/install-macos.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
shims="$tmp/shims"
artifacts="$tmp/artifacts"
mkdir -p "$shims" "$artifacts"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_file() { [[ -f "$1" ]] || fail "missing file: $1"; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$1 does not contain: $2"; }

cat >"$shims/launchctl" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"${LAUNCHCTL_LOG:?}"
EOF
chmod +x "$shims/launchctl"
export PATH="$shims:$PATH"
export LAUNCHCTL_LOG="$tmp/launchctl.log"

for binary in webport webportctl webport-dns; do
	printf '#!/bin/sh\nexit 0\n' >"$artifacts/$binary"
	chmod +x "$artifacts/$binary"
done
cat >"$artifacts/traefik" <<'EOF'
#!/bin/sh
case "${1:-}" in
	version) printf 'Version: v3.7.1\n' ;;
	healthcheck) printf 'OK\n' ;;
esac
exit 0
EOF
chmod +x "$artifacts/traefik"
credentials="$tmp/cloudflare.env"
printf 'CF_DNS_API_TOKEN=test-secret\n' >"$credentials"

root="$tmp/full"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --provider cloudflare --base-domain dev.example.com \
	--credentials-file "$credentials" --webport-source local --traefik-source local \
	--artifact-dir "$artifacts" --non-interactive --yes
assert_file "$root/usr/local/bin/traefik"
assert_file "$root/usr/local/etc/traefik/.webport-managed"
assert_file "$root/usr/local/etc/traefik/traefik.yml"
assert_file "$root/usr/local/libexec/webport/run-traefik"
assert_file "$root/Library/LaunchDaemons/com.webport.traefik.plist"
assert_file "$root/Library/LaunchDaemons/com.webport.webport.plist"
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH=/usr/local/etc/traefik/dynamic/webport.yml"
assert_contains "$LAUNCHCTL_LOG" "kickstart -k system/com.webport.traefik"
assert_contains "$LAUNCHCTL_LOG" "kickstart -k system/com.webport.webport"

root="$tmp/migrate"
mkdir -p "$root/usr/local/etc/caddy" "$root/usr/local/bin" "$root/Library/LaunchDaemons"
printf 'managed-by=webport\n' >"$root/usr/local/etc/caddy/.webport-managed"
printf 'CLOUDFLARE_API_TOKEN=old-secret\n' >"$root/usr/local/etc/caddy/caddy.env"
printf '#!/bin/sh\nexit 0\n' >"$root/usr/local/bin/caddy"
chmod +x "$root/usr/local/bin/caddy"
printf '<plist/>\n' >"$root/Library/LaunchDaemons/com.webport.caddy.plist"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --provider cloudflare --base-domain dev.example.com \
	--webport-source local --traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
[[ ! -e "$root/usr/local/bin/caddy" ]] || fail "managed Caddy binary was not removed"
assert_contains "$root/usr/local/etc/traefik/traefik.env" "CF_DNS_API_TOKEN=old-secret"

printf 'macOS installer tests passed\n'
