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
export SECURITY_LOG="$tmp/security.log"

cat >"$shims/security" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"${SECURITY_LOG:?}"
case "${1:-}" in
	find-certificate)
		[[ -z "${SECURITY_EXISTING_CERT:-}" ]] || cat "$SECURITY_EXISTING_CERT"
		[[ -n "${SECURITY_EXISTING_CERT:-}" ]] && exit 0
		exit 1
		;;
esac
EOF
chmod +x "$shims/security"

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

skill_home="$tmp/skill-home"
HOME="$skill_home" "$INSTALLER" --ai-skill --non-interactive --yes
for skill_path in \
	"$skill_home/.codex/skills/webport-development/SKILL.md" \
	"$skill_home/.config/opencode/skills/webport-development/SKILL.md" \
	"$skill_home/.pi/agent/skills/webport-development/SKILL.md" \
	"$skill_home/.claude/skills/webport-development/SKILL.md"; do
	assert_file "$skill_path"
	assert_contains "$skill_path" "name: webport-development"
done

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
assert_contains "$root/Library/LaunchDaemons/com.webport.webport.plist" "<string>_webport</string>"
assert_contains "$LAUNCHCTL_LOG" "kickstart -k system/com.webport.traefik"
assert_contains "$LAUNCHCTL_LOG" "kickstart -k system/com.webport.webport"

root="$tmp/upgrade"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --provider cloudflare --base-domain dev.example.com \
	--credentials-file "$credentials" --webport-source local --traefik-source local \
	--artifact-dir "$artifacts" --non-interactive --yes
cp "$(command -v bash)" "$root/usr/local/bin/webport"
"$root/usr/local/bin/webport" -c 'while :; do sleep 1; done' &
old_cli_pid=$!
sleep 0.1
kill -0 "$old_cli_pid" || fail "old webport CLI did not stay running"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--upgrade --webport-source local --traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
kill "$old_cli_pid"
wait "$old_cli_pid" 2>/dev/null || true
cmp -s "$artifacts/webport" "$root/usr/local/bin/webport" ||
	fail "upgrade did not replace the webport CLI"
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_BASE_DOMAIN=dev.example.com"
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_TLS_MODE=acme"
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_DNS_PROVIDER=cloudflare"
assert_contains "$root/usr/local/etc/traefik/traefik.env" "CF_DNS_API_TOKEN=test-secret"

root="$tmp/local-ca"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --tls-mode local-ca --base-domain webport.localhost \
	--webport-source local --traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_TLS_MODE=local-ca"
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_LOCAL_CA_DIR=/usr/local/etc/traefik/dynamic/webport-pki"
if grep -Fq "certificatesResolvers:" "$root/usr/local/etc/traefik/traefik.yml"; then
	fail "local-CA Traefik configuration contains an ACME resolver"
fi

root="$tmp/local-ca-trusted"
mkdir -p "$root/usr/local/etc/traefik/dynamic/webport-pki"
printf '%s\n' 'test CA' >"$root/usr/local/etc/traefik/dynamic/webport-pki/ca.crt"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --tls-mode local-ca --trust-local-ca \
	--base-domain webport.localhost --webport-source local \
	--traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
assert_contains "$SECURITY_LOG" "add-trusted-cert -d -r trustRoot"
assert_contains "$SECURITY_LOG" "/usr/local/etc/traefik/dynamic/webport-pki/ca.crt"

trusted_adds_before=$(grep -c 'add-trusted-cert' "$SECURITY_LOG")
SECURITY_EXISTING_CERT="$root/usr/local/etc/traefik/dynamic/webport-pki/ca.crt" \
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --tls-mode local-ca --trust-local-ca \
	--base-domain webport.localhost --webport-source local \
	--traefik-source local --artifact-dir "$artifacts" \
	--non-interactive --yes
trusted_adds_after=$(grep -c 'add-trusted-cert' "$SECURITY_LOG")
[[ "$trusted_adds_before" == "$trusted_adds_after" ]] ||
	fail "idempotent trust added a duplicate certificate"

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
