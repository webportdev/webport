#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
INSTALLER="$SCRIPT_DIR/install-macos.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
shims="$tmp/shims"
mkdir -p "$shims"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
assert_file() { [[ -f "$1" ]] || fail "missing file: $1"; }
assert_contains() { grep -Fq -- "$2" "$1" || fail "$1 does not contain: $2"; }
assert_not_contains() { grep -Fq -- "$2" "$1" && fail "$1 unexpectedly contains: $2" || true; }
file_mode() {
	if [[ "$(uname -s)" == Darwin ]]; then stat -f %Lp "$1"; else stat -c %a "$1"; fi
}
assert_mode() { [[ "$(file_mode "$1")" == "$2" ]] || fail "$1 has mode $(file_mode "$1"), expected $2"; }
expect_failure() {
	if "$@" >/dev/null 2>&1; then
		fail "command unexpectedly succeeded: $*"
	fi
}

cat >"$shims/go" <<'EOF'
#!/usr/bin/env bash
set -eu
if [[ "$1" == build ]]; then
	while (($#)); do
		if [[ "$1" == -o ]]; then output=$2; break; fi
		shift
	done
	mkdir -p "$(dirname "$output")"
	printf '#!/bin/sh\n[ -z "${DNS_SYNC_LOG:-}" ] || printf "%%s\\n" "$*" >>"$DNS_SYNC_LOG"\nexit 0\n' >"$output"
	chmod +x "$output"
elif [[ "$1" == install ]]; then
	mkdir -p "$GOBIN"
	cat >"$GOBIN/xcaddy" <<'INNER'
#!/usr/bin/env bash
set -eu
while (($#)); do
	case "$1" in
		--output) output=$2; shift 2 ;;
		*) shift ;;
	esac
done
cat >"$output" <<'CADDY'
#!/usr/bin/env bash
case "${1:-}" in
	list-modules)
		printf '%s\n' dns.providers.cloudflare dns.providers.digitalocean dns.providers.route53 dns.providers.acmedns
		;;
	validate) exit 0 ;;
	*) exit 0 ;;
esac
CADDY
chmod +x "$output"
INNER
	chmod +x "$GOBIN/xcaddy"
else
	exit 1
fi
EOF
chmod +x "$shims/go"

cat >"$shims/launchctl" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"${LAUNCHCTL_LOG:?}"
if [[ "${LAUNCHCTL_FAIL_CADDY:-0}" == 1 && "$*" == "kickstart -k system/com.webport.caddy" ]]; then
	exit 1
fi
exit 0
EOF
chmod +x "$shims/launchctl"

cat >"$shims/plutil" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$shims/plutil"

cat >"$shims/docker" <<'EOF'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"${DOCKER_LOG:?}"
out_dir=
out_name=
while (($#)); do
	case "$1" in
		-v)
			case "${2:-}" in
				*:/out) out_dir=${2%:/out} ;;
			esac
			shift 2
			;;
		*)
			if [[ "$1" =~ --output[[:space:]]\"/out/([^\"]+)\" ]]; then
				out_name=${BASH_REMATCH[1]}
			fi
			shift
			;;
	esac
done
[[ -n "$out_dir" && -n "$out_name" ]] || exit 1
mkdir -p "$out_dir"
cat >"$out_dir/$out_name" <<'CADDY'
#!/usr/bin/env bash
case "${1:-}" in
	list-modules)
		printf '%s\n' dns.providers.cloudflare dns.providers.digitalocean dns.providers.route53 dns.providers.acmedns
		;;
	validate) exit 0 ;;
	*) exit 0 ;;
esac
CADDY
chmod +x "$out_dir/$out_name"
EOF
chmod +x "$shims/docker"

export PATH="$shims:$PATH"
export LAUNCHCTL_LOG="$tmp/launchctl.log"
export DNS_SYNC_LOG="$tmp/dns-sync.log"
export DOCKER_LOG="$tmp/docker.log"
credentials="$tmp/cloudflare.env"
printf 'CLOUDFLARE_API_TOKEN=test-secret\n' >"$credentials"

dry_run_log="$tmp/dry-run.log"
WEBPORT_INSTALL_ROOT="$tmp/dry-run-root" "$INSTALLER" --mode webport --provider cloudflare --base-domain example.com \
	--non-interactive --yes --dry-run >"$dry_run_log"
assert_contains "$dry_run_log" "dry-run: go build"
assert_contains "$dry_run_log" "dry-run: install"
assert_contains "$dry_run_log" "dry-run: launchctl"
assert_not_contains "$dry_run_log" "dry-run: sudo go"

for mode in webport caddy full; do
	args=(--mode "$mode" --provider cloudflare --non-interactive --yes --dry-run)
	[[ "$mode" == caddy ]] || args+=(--base-domain example.com)
	[[ "$mode" == webport ]] || args+=(--credentials-file "$credentials")
	WEBPORT_INSTALL_ROOT="$tmp/dry-$mode" "$INSTALLER" "${args[@]}" >/dev/null
done

expect_failure "$INSTALLER" --mode full --provider custom --base-domain example.com --non-interactive --yes --dry-run
expect_failure "$INSTALLER" --mode full --provider cloudflare --base-domain example.com --non-interactive --yes --dry-run

local_artifacts="$tmp/local-artifacts"
mkdir -p "$local_artifacts"
for binary in webport webportctl webport-dns; do
	printf '#!/bin/sh\nexit 0\n' >"$local_artifacts/$binary"
	chmod +x "$local_artifacts/$binary"
done
fail_go="$tmp/fail-go"
mkdir -p "$fail_go"
printf '#!/bin/sh\nexit 99\n' >"$fail_go/go"
chmod +x "$fail_go/go"
root="$tmp/root-local-webport"
mkdir -p "$root/usr/local/bin"
cat >"$root/usr/local/bin/caddy" <<'EOF'
#!/bin/sh
printf '%s\n' dns.providers.cloudflare
EOF
chmod +x "$root/usr/local/bin/caddy"
PATH="$fail_go:$PATH" WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode webport --provider cloudflare --base-domain example.com \
	--webport-source local --artifact-dir "$local_artifacts" --non-interactive --yes
assert_file "$root/usr/local/bin/webport"
assert_file "$root/usr/local/bin/webportctl"
assert_file "$root/usr/local/bin/webport-dns"

root="$tmp/root-full"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --provider cloudflare --base-domain example.com \
	--credentials-file "$credentials" --non-interactive --yes
assert_file "$root/usr/local/bin/caddy"
assert_file "$root/usr/local/bin/webport"
assert_file "$root/usr/local/bin/webportctl"
assert_file "$root/usr/local/bin/webport-dns"
assert_file "$root/usr/local/etc/caddy/.webport-managed"
assert_file "$root/usr/local/etc/caddy/caddy.env"
assert_file "$root/usr/local/etc/webport/webport.env"
assert_file "$root/Library/LaunchDaemons/com.webport.caddy.plist"
assert_file "$root/Library/LaunchDaemons/com.webport.webport.plist"
assert_mode "$root/usr/local/etc/caddy/caddy.env" 600
assert_mode "$root/usr/local/etc/webport/webport.env" 600
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_LISTEN_HOST=127.0.0.1"
assert_contains "$LAUNCHCTL_LOG" "kickstart -k system/com.webport.caddy"
assert_contains "$LAUNCHCTL_LOG" "kickstart -k system/com.webport.webport"
assert_not_contains "$root/usr/local/libexec/webport/run-caddy" "--environ"
assert_contains "$DOCKER_LOG" "golang:1.25"

root="$tmp/root-dns"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode full --provider cloudflare --base-domain dev.example.com \
	--credentials-file "$credentials" --dns-ipv4 192.0.2.10 --dns-ipv6 2001:db8::10 \
	--dns-zone example.com --non-interactive --yes
assert_file "$root/usr/local/bin/webport-dns"
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_DNS_CREDENTIALS_FILE=/usr/local/etc/caddy/caddy.env"
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_DNS_ZONE=example.com"
assert_contains "$DNS_SYNC_LOG" "sync --config"

root="$tmp/root-unmanaged"
mkdir -p "$root/usr/local/bin"
printf '#!/bin/sh\nexit 0\n' >"$root/usr/local/bin/caddy"
chmod +x "$root/usr/local/bin/caddy"
expect_failure env WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode caddy --provider cloudflare --credentials-file "$credentials" --non-interactive --yes

root="$tmp/root-webport"
mkdir -p "$root/usr/local/bin"
cat >"$root/usr/local/bin/caddy" <<'EOF'
#!/bin/sh
printf '%s\n' dns.providers.route53
EOF
chmod +x "$root/usr/local/bin/caddy"
WEBPORT_INSTALL_ROOT="$root" "$INSTALLER" \
	--mode webport --provider route53 --base-domain example.com --non-interactive --yes
assert_contains "$root/usr/local/etc/webport/webport.env" "WEBPORT_TLS_DNS_TOKEN_ENV_VAR="

root="$tmp/root-rollback"
mkdir -p "$root/usr/local/bin" "$root/usr/local/etc/caddy"
printf 'managed-by=webport\n' >"$root/usr/local/etc/caddy/.webport-managed"
printf '#!/bin/sh\nprintf old-binary\n' >"$root/usr/local/bin/caddy"
chmod +x "$root/usr/local/bin/caddy"
expect_failure env WEBPORT_INSTALL_ROOT="$root" LAUNCHCTL_FAIL_CADDY=1 "$INSTALLER" \
	--mode caddy --provider cloudflare --credentials-file "$credentials" --non-interactive --yes
assert_contains "$root/usr/local/bin/caddy" "old-binary"

root="$tmp/root-first-install-failure"
expect_failure env WEBPORT_INSTALL_ROOT="$root" LAUNCHCTL_FAIL_CADDY=1 "$INSTALLER" \
	--mode caddy --provider cloudflare --credentials-file "$credentials" --non-interactive --yes
[[ ! -e "$root/usr/local/bin/caddy" ]] || fail "failed first install left a Caddy binary behind"

printf 'macOS installer tests passed\n'
