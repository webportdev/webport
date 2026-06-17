#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
# shellcheck source=versions.env
source "$SCRIPT_DIR/versions.env"
APPLY=0
[[ ${1:-} != --apply ]] || APPLY=1

latest() { go list -m -f '{{.Version}}' "$1@latest"; }
validate() {
	local binary=$1 config=$2 token=$3
	if [[ "$token" == none ]]; then
		"$binary" validate --config "$config"
	else
		env "$token=0000000000000000000000000000000000000000" "$binary" validate --config "$config"
	fi
}

declare -A modules=(
	[CADDY_VERSION]=github.com/caddyserver/caddy/v2
	[XCADDY_VERSION]=github.com/caddyserver/xcaddy
	[CLOUDFLARE_MODULE_VERSION]=github.com/caddy-dns/cloudflare
	[DIGITALOCEAN_MODULE_VERSION]=github.com/caddy-dns/digitalocean
	[ROUTE53_MODULE_VERSION]=github.com/caddy-dns/route53
)

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
for key in CADDY_VERSION XCADDY_VERSION CLOUDFLARE_MODULE_VERSION DIGITALOCEAN_MODULE_VERSION ROUTE53_MODULE_VERSION; do
	value=$(latest "${modules[$key]}")
	printf '%s=%s\n' "$key" "$value" >>"$tmp"
	printf '%-30s pinned=%-35s latest=%s\n' "$key" "${!key}" "$value"
done

(( APPLY )) || exit 0

build_root=$(mktemp -d)
trap 'rm -f "$tmp"; rm -rf "$build_root"' EXIT
# shellcheck source=/dev/null
source "$tmp"
GOBIN="$build_root/bin" go install "github.com/caddyserver/xcaddy/cmd/xcaddy@$XCADDY_VERSION"
for spec in \
	"cloudflare github.com/caddy-dns/cloudflare@$CLOUDFLARE_MODULE_VERSION CLOUDFLARE_API_TOKEN" \
	"digitalocean github.com/caddy-dns/digitalocean@$DIGITALOCEAN_MODULE_VERSION DO_AUTH_TOKEN" \
	"route53 github.com/caddy-dns/route53@$ROUTE53_MODULE_VERSION none"; do
	read -r provider module token <<<"$spec"
	binary="$build_root/caddy-$provider"
	"$build_root/bin/xcaddy" build "$CADDY_VERSION" --with "$module" --output "$binary"
	"$binary" list-modules | grep -Fxq "dns.providers.$provider"
	if [[ "$token" == none ]]; then directive="dns $provider"; else directive="dns $provider {env.$token}"; fi
	printf 'example.invalid {\n tls {\n  %s\n }\n}\n' "$directive" >"$build_root/Caddyfile"
	validate "$binary" "$build_root/Caddyfile" "$token"
done
(cd "$REPO_DIR" && go test ./...)
install -m 644 "$tmp" "$SCRIPT_DIR/versions.env"
printf 'Updated %s after all curated builds and tests passed.\n' "$SCRIPT_DIR/versions.env"
