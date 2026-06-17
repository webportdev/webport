#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
# shellcheck source=versions.env
source "$SCRIPT_DIR/versions.env"

PROVIDER=
MODULE_PATH=
MODULE_VERSION=
PROVIDER_NAME=
TOKEN_ENV_VAR=
OUTPUT=
TARGET_OS=
TARGET_ARCH=
DRY_RUN=0

usage() {
	cat <<'EOF'
Usage: scripts/build-caddy-docker.sh [options]

Build a custom Caddy binary with a DNS provider module using Docker.

Providers:
  --provider cloudflare|digitalocean|route53|custom
Custom provider:
  --module-path PATH --module-version VERSION --provider-name NAME [--token-env-var NAME]
Version overrides:
  --caddy-version VERSION --module-version VERSION
Output:
  --output PATH
  --os linux|darwin
  --arch amd64|arm64
Automation:
  --dry-run

Environment:
  WEBPORT_CADDY_BUILDER_IMAGE  Docker image to use, default golang:1.25
EOF
}

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*"; }

host_os() {
	case "$(uname -s)" in
		Linux) printf 'linux' ;;
		Darwin) printf 'darwin' ;;
		*) die "unsupported host OS: $(uname -s)" ;;
	esac
}

host_arch() {
	case "$(uname -m)" in
		x86_64|amd64) printf 'amd64' ;;
		arm64|aarch64) printf 'arm64' ;;
		*) die "unsupported host architecture: $(uname -m)" ;;
	esac
}

while (($#)); do
	case "$1" in
		--provider) PROVIDER=${2:-}; shift 2 ;;
		--module-path) MODULE_PATH=${2:-}; shift 2 ;;
		--module-version) MODULE_VERSION=${2:-}; shift 2 ;;
		--caddy-version) CADDY_VERSION=${2:-}; shift 2 ;;
		--provider-name) PROVIDER_NAME=${2:-}; shift 2 ;;
		--token-env-var) TOKEN_ENV_VAR=${2:-}; shift 2 ;;
		--output) OUTPUT=${2:-}; shift 2 ;;
		--os) TARGET_OS=${2:-}; shift 2 ;;
		--arch) TARGET_ARCH=${2:-}; shift 2 ;;
		--dry-run) DRY_RUN=1; shift ;;
		--help|-h) usage; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

[[ -n "$PROVIDER" ]] || die "--provider is required"
[[ "$PROVIDER" =~ ^(cloudflare|digitalocean|route53|custom)$ ]] || die "invalid provider: $PROVIDER"

case "$PROVIDER" in
	cloudflare)
		MODULE_PATH=github.com/caddy-dns/cloudflare
		MODULE_VERSION=${MODULE_VERSION:-$CLOUDFLARE_MODULE_VERSION}
		PROVIDER_NAME=cloudflare
		TOKEN_ENV_VAR=CLOUDFLARE_API_TOKEN
		;;
	digitalocean)
		MODULE_PATH=github.com/caddy-dns/digitalocean
		MODULE_VERSION=${MODULE_VERSION:-$DIGITALOCEAN_MODULE_VERSION}
		PROVIDER_NAME=digitalocean
		TOKEN_ENV_VAR=DO_AUTH_TOKEN
		;;
	route53)
		MODULE_PATH=github.com/caddy-dns/route53
		MODULE_VERSION=${MODULE_VERSION:-$ROUTE53_MODULE_VERSION}
		PROVIDER_NAME=route53
		TOKEN_ENV_VAR=
		;;
	custom)
		[[ -n "$MODULE_PATH" && -n "$MODULE_VERSION" && -n "$PROVIDER_NAME" ]] ||
			die "custom provider requires --module-path, --module-version, and --provider-name"
		[[ "$MODULE_PATH" =~ ^[A-Za-z0-9._~/-]+$ ]] || die "invalid custom module path"
		[[ "$PROVIDER_NAME" =~ ^[A-Za-z0-9_-]+$ ]] || die "invalid custom provider name"
		[[ -z "$TOKEN_ENV_VAR" || "$TOKEN_ENV_VAR" =~ ^[A-Z_][A-Z0-9_]*$ ]] || die "invalid token environment variable"
		;;
esac

TARGET_OS=${TARGET_OS:-$(host_os)}
TARGET_ARCH=${TARGET_ARCH:-$(host_arch)}
[[ "$TARGET_OS" =~ ^(linux|darwin)$ ]] || die "invalid target OS: $TARGET_OS"
[[ "$TARGET_ARCH" =~ ^(amd64|arm64)$ ]] || die "invalid target architecture: $TARGET_ARCH"
[[ "$CADDY_VERSION" =~ ^[A-Za-z0-9._~+/-]+$ ]] || die "invalid Caddy version"
[[ "$MODULE_VERSION" =~ ^[A-Za-z0-9._~+/-]+$ ]] || die "invalid module version"

if [[ -z "$OUTPUT" ]]; then
	OUTPUT="$REPO_DIR/build/caddy-$PROVIDER-$TARGET_OS-$TARGET_ARCH"
fi

output_dir=$(dirname -- "$OUTPUT")
case "$output_dir" in
	/*) ;;
	*) output_dir="$PWD/$output_dir" ;;
esac
output_name=$(basename -- "$OUTPUT")
module="$MODULE_PATH@$MODULE_VERSION"
image=${WEBPORT_CADDY_BUILDER_IMAGE:-golang:1.25}

if (( DRY_RUN )); then
	log "dry-run: docker run --rm -e GOOS=$TARGET_OS -e GOARCH=$TARGET_ARCH -v $output_dir:/out $image xcaddy build $CADDY_VERSION --with $module --output /out/$output_name"
	exit 0
fi

command -v docker >/dev/null 2>&1 || die "docker is required to build Caddy without a local Go toolchain"
mkdir -p "$output_dir"

docker run --rm \
	-e GOOS="$TARGET_OS" \
	-e GOARCH="$TARGET_ARCH" \
	-e CGO_ENABLED=0 \
	-e GOBIN=/tmp/webport-bin \
	-v "$output_dir:/out" \
	"$image" \
	/bin/sh -ceu '
		go install "github.com/caddyserver/xcaddy/cmd/xcaddy@'"$XCADDY_VERSION"'"
		/tmp/webport-bin/xcaddy build "'"$CADDY_VERSION"'" --with "'"$module"'" --output "/out/'"$output_name"'"
	'

chmod +x "$OUTPUT"

if [[ "$TARGET_OS" == "$(host_os)" && "$TARGET_ARCH" == "$(host_arch)" ]]; then
	"$OUTPUT" list-modules | grep -Fxq "dns.providers.$PROVIDER_NAME" ||
		die "built Caddy does not contain dns.providers.$PROVIDER_NAME"
	validation_config=$(mktemp)
	trap 'rm -f "$validation_config"' EXIT
	if [[ -n "$TOKEN_ENV_VAR" ]]; then
		directive="dns $PROVIDER_NAME {env.$TOKEN_ENV_VAR}"
		printf 'example.invalid {\n tls {\n  %s\n }\n}\n' "$directive" >"$validation_config"
		env "$TOKEN_ENV_VAR=0000000000000000000000000000000000000000" "$OUTPUT" validate --config "$validation_config"
	else
		directive="dns $PROVIDER_NAME"
		printf 'example.invalid {\n tls {\n  %s\n }\n}\n' "$directive" >"$validation_config"
		"$OUTPUT" validate --config "$validation_config"
	fi
else
	log "Skipping executable validation for cross-built $TARGET_OS/$TARGET_ARCH binary."
fi

log "Built $OUTPUT"
