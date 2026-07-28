#!/usr/bin/env bash
set -euo pipefail

TRAEFIK_BIN=${TRAEFIK_BIN:-}
[[ -n "$TRAEFIK_BIN" && -x "$TRAEFIK_BIN" ]] || {
	printf 'error: set TRAEFIK_BIN to an executable Traefik binary\n' >&2
	exit 2
}

REPO_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
traefik_pid=
webport_pid=
backend_pid=

cleanup() {
	[[ -z "$webport_pid" ]] || kill "$webport_pid" >/dev/null 2>&1 || true
	[[ -z "$traefik_pid" ]] || kill "$traefik_pid" >/dev/null 2>&1 || true
	[[ -z "$backend_pid" ]] || kill "$backend_pid" >/dev/null 2>&1 || true
	if [[ "${KEEP_TMP:-0}" == 1 ]]; then
		printf 'integration test files retained at %s\n' "$tmp" >&2
	else
		rm -rf "$tmp"
	fi
}
trap cleanup EXIT

mkdir -p "$tmp/dynamic" "$tmp/backend"
printf 'proxied by Traefik\n' >"$tmp/backend/index.html"
printf '{}' >"$tmp/acme.json"
chmod 600 "$tmp/acme.json"

cat >"$tmp/traefik.yml" <<EOF
global:
  checkNewVersion: false
  sendAnonymousUsage: false
entryPoints:
  websecure:
    address: "127.0.0.1:18443"
  ping:
    address: "127.0.0.1:18082"
providers:
  file:
    directory: "$tmp/dynamic"
    watch: true
certificatesResolvers:
  webport:
    acme:
      email: test@example.com
      storage: "$tmp/acme.json"
      caServer: "http://127.0.0.1:9/directory"
      tlsChallenge: {}
ping:
  entryPoint: ping
EOF

GOCACHE=${GOCACHE:-/tmp/webport-go-cache} go build -o "$tmp/webport" "$REPO_DIR/cmd/webport"
(
	cd "$tmp/backend"
	python3 -m http.server 18081 --bind 127.0.0.1 >"$tmp/backend.log" 2>&1
) &
backend_pid=$!

WEBPORT_BASE_DOMAIN=example.com \
WEBPORT_PORT=18080 \
WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH="$tmp/dynamic/webport.yml" \
"$tmp/webport" >"$tmp/webport.log" 2>&1 &
webport_pid=$!

"$TRAEFIK_BIN" --configFile="$tmp/traefik.yml" >"$tmp/traefik.log" 2>&1 &
traefik_pid=$!

for _ in {1..40}; do
	if curl --noproxy '*' -fsS http://127.0.0.1:18082/ping >/dev/null 2>&1; then break; fi
	sleep 0.25
done
curl --noproxy '*' -fsS http://127.0.0.1:18082/ping >/dev/null

curl --noproxy '*' -fsS -X POST http://127.0.0.1:18080/routes \
	-H 'Content-Type: application/json' \
	-d '{"project":"app","branch":"main","port":18081}' >/dev/null

for _ in {1..40}; do
	if curl --noproxy '*' -kfsS --resolve app-main.example.com:18443:127.0.0.1 \
		https://app-main.example.com:18443/ 2>/dev/null | grep -q 'proxied by Traefik'; then
		break
	fi
	sleep 0.25
done
curl --noproxy '*' -kfsS --resolve app-main.example.com:18443:127.0.0.1 \
	https://app-main.example.com:18443/ | grep -q 'proxied by Traefik'

curl --noproxy '*' -fsS -X DELETE http://127.0.0.1:18080/routes/app:main >/dev/null
for _ in {1..40}; do
	status=$(curl --noproxy '*' -ksS -o /dev/null -w '%{http_code}' \
		--resolve app-main.example.com:18443:127.0.0.1 \
		https://app-main.example.com:18443/)
	[[ "$status" == 404 ]] && break
	sleep 0.25
done
[[ "$status" == 404 ]] || {
	printf 'error: deleted route still returned HTTP %s\n' "$status" >&2
	exit 1
}

printf 'Traefik integration test passed\n'
