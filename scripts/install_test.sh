#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
INSTALLER="$SCRIPT_DIR/install.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
shims="$tmp/shims"
artifacts="$tmp/artifacts"
mkdir -p "$shims" "$artifacts"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_file() { [[ -f "$1" ]] || fail "missing file: $1"; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$1 does not contain: $2"; }
expect_failure() { "$@" >/dev/null 2>&1 && fail "command unexpectedly succeeded: $*" || true; }

cat >"$shims/systemctl" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"${SYSTEMCTL_LOG:?}"
if [[ "${FAIL_TRAEFIK_START:-0}" == 1 &&
	( "$*" == "enable --now traefik.service" || "$*" == "start traefik.service" ) ]]; then
	exit 1
fi
EOF
chmod +x "$shims/systemctl"
export PATH="$shims:$PATH"
export SYSTEMCTL_LOG="$tmp/systemctl.log"

for binary in webport webportctl webport-dns; do
	cat >"$artifacts/$binary" <<'EOF'
#!/bin/sh
[[ -z "${DNS_SYNC_LOG:-}" ]] || printf '%s\n' "$*" >>"$DNS_SYNC_LOG"
exit 0
EOF
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

cloudflare="$tmp/cloudflare.env"
printf 'CF_DNS_API_TOKEN=test-secret\n' >"$cloudflare"
digitalocean="$tmp/digitalocean.env"
printf 'DO_AUTH_TOKEN=test-secret\n' >"$digitalocean"
generic="$tmp/generic.env"
printf 'HETZNER_API_KEY=test-secret\n' >"$generic"

root="$tmp/full"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --provider cloudflare --base-domain dev.example.com \
	--credentials-file "$cloudflare" --webport-source local --traefik-source local \
	--artifact-dir "$artifacts" --non-interactive --yes
assert_file "$root/usr/local/bin/traefik"
assert_file "$root/usr/local/bin/webport"
assert_file "$root/etc/traefik/.webport-managed"
assert_file "$root/etc/traefik/traefik.yml"
assert_file "$root/etc/traefik/traefik.env"
assert_file "$root/etc/systemd/system/traefik.service"
assert_file "$root/etc/systemd/system/webport.service"
assert_file "$root/etc/systemd/system/webport-stack.target"
assert_contains "$root/etc/traefik/traefik.yml" "provider: cloudflare"
assert_contains "$root/etc/webport/webport.env" "WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH=/etc/traefik/dynamic/webport.yml"
assert_contains "$root/etc/systemd/system/webport.service" "Requires=traefik.service"
assert_contains "$root/etc/systemd/system/webport.service" "PartOf=webport-stack.target"
assert_contains "$root/etc/systemd/system/traefik.service" "PartOf=webport-stack.target"
assert_contains "$root/etc/systemd/system/webport-stack.target" "Requires=traefik.service webport.service"
assert_contains "$SYSTEMCTL_LOG" "start traefik.service"
assert_contains "$SYSTEMCTL_LOG" "start webport.service"
assert_contains "$SYSTEMCTL_LOG" "enable --now webport-stack.target"

root="$tmp/existing-traefik"
mkdir -p "$root/usr/local/bin"
cp "$artifacts/traefik" "$root/usr/local/bin/traefik"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode webport --base-domain dev.example.com --webport-source local \
	--artifact-dir "$artifacts" --non-interactive --yes
assert_file "$root/usr/local/bin/webport"
assert_file "$root/etc/systemd/system/webport-stack.target"
assert_contains "$root/etc/webport/webport.env" "WEBPORT_DNS_PROVIDER="

root="$tmp/local-ca"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --tls-mode local-ca --base-domain webport.localhost \
	--webport-source local --traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
assert_contains "$root/etc/webport/webport.env" "WEBPORT_TLS_MODE=local-ca"
assert_contains "$root/etc/webport/webport.env" "WEBPORT_LOCAL_CA_DIR=/etc/traefik/dynamic/webport-pki"
assert_file "$root/etc/traefik/traefik.env"
if grep -Fq "certificatesResolvers:" "$root/etc/traefik/traefik.yml"; then
	fail "local-CA Traefik configuration contains an ACME resolver"
fi

root="$tmp/generic"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode traefik --provider hetzner --credentials-file "$generic" \
	--traefik-source local --artifact-dir "$artifacts" --non-interactive --yes
assert_contains "$root/etc/traefik/traefik.yml" "provider: hetzner"
assert_contains "$root/etc/traefik/traefik.env" "HETZNER_API_KEY=test-secret"
assert_contains "$SYSTEMCTL_LOG" "enable --now traefik.service"
expect_failure env WEBPORT_INSTALL_ROOT="$tmp/generic-dns" "$INSTALLER" \
	--mode full --provider hetzner --base-domain example.com --credentials-file "$generic" \
	--webport-source local --traefik-source local --artifact-dir "$artifacts" \
	--dns-ipv4 192.0.2.1 --non-interactive --yes

release_dir="$tmp/traefik-release"
release_archive_dir="$tmp/traefik-release-content"
mkdir -p "$release_dir" "$release_archive_dir"
cp "$artifacts/traefik" "$release_archive_dir/traefik"
tar -C "$release_archive_dir" -czf "$release_dir/traefik_v3.7.1_linux_amd64.tar.gz" traefik
(
	cd "$release_dir"
	sha256sum traefik_v3.7.1_linux_amd64.tar.gz >traefik_v3.7.1_checksums.txt
)
root="$tmp/upstream-release"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode traefik --provider digitalocean --credentials-file "$digitalocean" \
	--traefik-source release --traefik-release-base-url "file://$release_dir" \
	--non-interactive --yes
assert_file "$root/usr/local/bin/traefik"

root="$tmp/migrate"
mkdir -p "$root/etc/caddy" "$root/etc/systemd/system" "$root/usr/local/bin"
printf 'managed-by=webport\n' >"$root/etc/caddy/.webport-managed"
printf 'CLOUDFLARE_API_TOKEN=old-secret\n' >"$root/etc/caddy/caddy.env"
printf '#!/bin/sh\nexit 0\n' >"$root/usr/local/bin/caddy"
chmod +x "$root/usr/local/bin/caddy"
printf '[Service]\n' >"$root/etc/systemd/system/caddy.service"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --provider cloudflare --base-domain dev.example.com \
	--webport-source local --traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
[[ ! -e "$root/usr/local/bin/caddy" ]] || fail "managed Caddy binary was not removed"
[[ ! -e "$root/etc/caddy" ]] || fail "managed Caddy configuration was not removed"
assert_contains "$root/etc/traefik/traefik.env" "CF_DNS_API_TOKEN=old-secret"

root="$tmp/rollback"
mkdir -p "$root/etc/caddy" "$root/etc/systemd/system" "$root/usr/local/bin"
printf 'managed-by=webport\n' >"$root/etc/caddy/.webport-managed"
printf 'CLOUDFLARE_API_TOKEN=old-secret\n' >"$root/etc/caddy/caddy.env"
printf '#!/bin/sh\nexit 0\n' >"$root/usr/local/bin/caddy"
chmod +x "$root/usr/local/bin/caddy"
printf '[Service]\n' >"$root/etc/systemd/system/caddy.service"
expect_failure env FAIL_TRAEFIK_START=1 WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode traefik --provider cloudflare --traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
assert_file "$root/usr/local/bin/caddy"
assert_contains "$SYSTEMCTL_LOG" "start caddy.service"

root="$tmp/traefik-upgrade-rollback"
mkdir -p "$root/etc/traefik" "$root/etc/systemd/system" "$root/usr/local/bin"
printf 'managed-by=webport\n' >"$root/etc/traefik/.webport-managed"
printf 'old-config\n' >"$root/etc/traefik/traefik.yml"
printf 'DO_AUTH_TOKEN=old-secret\n' >"$root/etc/traefik/traefik.env"
printf 'old-service\n' >"$root/etc/systemd/system/traefik.service"
printf '#!/bin/sh\nprintf old-traefik\n' >"$root/usr/local/bin/traefik"
chmod +x "$root/usr/local/bin/traefik"
expect_failure env FAIL_TRAEFIK_START=1 WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode traefik --provider digitalocean --credentials-file "$digitalocean" \
	--traefik-source local --artifact-dir "$artifacts" --non-interactive --yes
assert_contains "$root/usr/local/bin/traefik" "old-traefik"
assert_contains "$root/etc/traefik/traefik.yml" "old-config"
assert_contains "$root/etc/systemd/system/traefik.service" "old-service"

expect_failure "$INSTALLER" --mode caddy --provider cloudflare --non-interactive --yes --dry-run
expect_failure "$INSTALLER" --mode traefik --provider cloudflare --caddy-source release --non-interactive --yes --dry-run
expect_failure "$INSTALLER" --mode full --tls-mode local-ca --trust-local-ca \
	--base-domain webport.localhost --non-interactive --yes --dry-run

printf 'Linux installer tests passed\n'
