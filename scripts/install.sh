#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
# shellcheck source=versions.env
source "$SCRIPT_DIR/versions.env"

MODE=
PROVIDER=
BASE_DOMAIN=
CREDENTIALS_FILE=
MODULE_PATH=
PROVIDER_NAME=
TOKEN_ENV_VAR=
MODULE_VERSION=
WEBPORT_SOURCE=${WEBPORT_SOURCE:-}
CADDY_SOURCE=${CADDY_SOURCE:-}
VERSION=${WEBPORT_VERSION:-}
RELEASE_BASE_URL=${WEBPORT_RELEASE_BASE_URL:-}
CADDY_RELEASE_VERSION=${WEBPORT_CADDY_RELEASE_VERSION:-latest}
CADDY_RELEASE_BASE_URL=${WEBPORT_CADDY_RELEASE_BASE_URL:-}
ARTIFACT_DIR=${WEBPORT_ARTIFACT_DIR:-}
DNS_IPV4=
DNS_IPV6=
DNS_ZONE=
CONFIGURE_DNS=0
DNS_CREDENTIALS_PATH=
NON_INTERACTIVE=0
ASSUME_YES=0
DRY_RUN=0
ROOT=${WEBPORT_INSTALL_ROOT:-/}
BUILD_DIR=
INTERACTIVE_CREDENTIALS=

if (( EUID == 0 )) && [[ "$ROOT" == / ]]; then
	printf 'error: do not run this installer as root; rerun it without sudo\n' >&2
	exit 1
fi

cleanup() {
	[[ -z "$BUILD_DIR" ]] || rm -rf "$BUILD_DIR"
	[[ -z "$INTERACTIVE_CREDENTIALS" ]] || rm -f "$INTERACTIVE_CREDENTIALS"
}
trap cleanup EXIT

usage() {
	cat <<'EOF'
Usage: scripts/install.sh [options]

Run as a regular user; the installer requests sudo only for system changes.

Modes:
  --mode webport|caddy|full
Providers:
  --provider cloudflare|digitalocean|route53|custom
Required for webport/full:
  --base-domain DOMAIN
Automated caddy/full or DNS sync credentials:
  --credentials-file FILE
Custom provider:
  --module-path PATH --provider-name NAME --token-env-var NAME
Version overrides:
  --caddy-version VERSION --module-version VERSION
Binaries:
  --webport-source release|local|build
  --caddy-source release|docker|local|build
  --version VERSION --release-base-url URL --artifact-dir DIR
  --caddy-release-version VERSION --caddy-release-base-url URL
Automation:
  --dns-ipv4 ADDRESS --dns-ipv6 ADDRESS --dns-zone ZONE
  --non-interactive --yes --dry-run

Secret values are never accepted as command-line arguments.
EOF
}

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*"; }
path() { printf '%s%s' "${ROOT%/}" "$1"; }

run() {
	if (( DRY_RUN )); then
		printf 'dry-run:'
		printf ' %q' "$@"
		printf '\n'
	else
		"$@"
	fi
}

privileged() {
	if (( DRY_RUN )); then
		printf 'dry-run:'
		[[ "$ROOT" == / ]] && printf ' sudo'
		printf ' %q' "$@"
		printf '\n'
	elif [[ "$ROOT" == / ]]; then
		sudo "$@"
	else
		"$@"
	fi
}

write_file() {
	local destination=$1 mode=$2 content=$3 tmp
	if (( DRY_RUN )); then
		if [[ "$ROOT" == / ]]; then
			log "dry-run: sudo write $destination (mode $mode)"
		else
			log "dry-run: write $destination (mode $mode)"
		fi
		return
	fi
	tmp=$(mktemp)
	printf '%s' "$content" >"$tmp"
	privileged install -D -m "$mode" "$tmp" "$destination"
	rm -f "$tmp"
}

ensure_file() {
	local destination=$1 mode=$2
	[[ -e "$destination" ]] && return
	write_file "$destination" "$mode" ""
}

while (($#)); do
	case "$1" in
		--mode) MODE=${2:-}; shift 2 ;;
		--provider) PROVIDER=${2:-}; shift 2 ;;
		--base-domain) BASE_DOMAIN=${2:-}; shift 2 ;;
		--credentials-file) CREDENTIALS_FILE=${2:-}; shift 2 ;;
		--caddy-version) CADDY_VERSION=${2:-}; shift 2 ;;
		--module-version) MODULE_VERSION=${2:-}; shift 2 ;;
		--module-path) MODULE_PATH=${2:-}; shift 2 ;;
		--provider-name) PROVIDER_NAME=${2:-}; shift 2 ;;
		--token-env-var) TOKEN_ENV_VAR=${2:-}; shift 2 ;;
		--webport-source) WEBPORT_SOURCE=${2:-}; shift 2 ;;
		--caddy-source) CADDY_SOURCE=${2:-}; shift 2 ;;
		--version) VERSION=${2:-}; shift 2 ;;
		--release-base-url) RELEASE_BASE_URL=${2:-}; shift 2 ;;
		--caddy-release-version) CADDY_RELEASE_VERSION=${2:-}; shift 2 ;;
		--caddy-release-base-url) CADDY_RELEASE_BASE_URL=${2:-}; shift 2 ;;
		--artifact-dir) ARTIFACT_DIR=${2:-}; shift 2 ;;
		--dns-ipv4) DNS_IPV4=${2:-}; CONFIGURE_DNS=1; shift 2 ;;
		--dns-ipv6) DNS_IPV6=${2:-}; CONFIGURE_DNS=1; shift 2 ;;
		--dns-zone) DNS_ZONE=${2:-}; shift 2 ;;
		--non-interactive) NON_INTERACTIVE=1; shift ;;
		--yes|-y) ASSUME_YES=1; shift ;;
		--dry-run) DRY_RUN=1; shift ;;
		--help|-h) usage; exit 0 ;;
		--*token*|--*secret*|--*credential-value*) die "secret values must be supplied interactively or through --credentials-file" ;;
		*) die "unknown argument: $1" ;;
	esac
done

prompt_value() {
	local variable=$1 prompt=$2 value
	[[ -t 0 ]] || die "$variable is required (stdin is not interactive)"
	read -r -p "$prompt: " value
	printf -v "$variable" '%s' "$value"
}

[[ -n "$MODE" ]] || { (( NON_INTERACTIVE )) && die "--mode is required with --non-interactive"; prompt_value MODE "Install mode (webport/caddy/full)"; }
[[ "$MODE" =~ ^(webport|caddy|full)$ ]] || die "invalid mode: $MODE"
[[ -n "$PROVIDER" ]] || { (( NON_INTERACTIVE )) && die "--provider is required with --non-interactive"; prompt_value PROVIDER "DNS provider (cloudflare/digitalocean/route53/custom)"; }
[[ "$PROVIDER" =~ ^(cloudflare|digitalocean|route53|custom)$ ]] || die "invalid provider: $PROVIDER"

if [[ "$MODE" != caddy && -z "$BASE_DOMAIN" ]]; then
	(( NON_INTERACTIVE )) && die "--base-domain is required with --non-interactive"
	prompt_value BASE_DOMAIN "Base domain"
fi
[[ -z "$BASE_DOMAIN" || "$BASE_DOMAIN" =~ ^[A-Za-z0-9.-]+$ ]] || die "invalid base domain: $BASE_DOMAIN"
[[ -z "$DNS_ZONE" || "$DNS_ZONE" =~ ^[A-Za-z0-9.-]+$ ]] || die "invalid DNS zone: $DNS_ZONE"

if [[ "$MODE" != caddy && "$NON_INTERACTIVE" == 0 && "$DRY_RUN" == 0 && "$CONFIGURE_DNS" == 0 ]]; then
	read -r -p "Configure wildcard DNS now? [Y/n] " answer
	if [[ ! "$answer" =~ ^[Nn]$ ]]; then
		CONFIGURE_DNS=1
		prompt_value DNS_IPV4 "Public IPv4 address"
		read -r -p "Public IPv6 address (optional): " DNS_IPV6
		read -r -p "Authoritative DNS zone override (optional): " DNS_ZONE
	fi
fi

case "$PROVIDER" in
	cloudflare)
		MODULE_PATH=github.com/caddy-dns/cloudflare
		PROVIDER_NAME=cloudflare
		TOKEN_ENV_VAR=CLOUDFLARE_API_TOKEN
		MODULE_VERSION=${MODULE_VERSION:-$CLOUDFLARE_MODULE_VERSION}
		;;
	digitalocean)
		MODULE_PATH=github.com/caddy-dns/digitalocean
		PROVIDER_NAME=digitalocean
		TOKEN_ENV_VAR=DO_AUTH_TOKEN
		MODULE_VERSION=${MODULE_VERSION:-$DIGITALOCEAN_MODULE_VERSION}
		;;
	route53)
		MODULE_PATH=github.com/caddy-dns/route53
		PROVIDER_NAME=route53
		TOKEN_ENV_VAR=
		MODULE_VERSION=${MODULE_VERSION:-$ROUTE53_MODULE_VERSION}
		;;
	custom)
		[[ -n "$MODULE_PATH" && -n "$PROVIDER_NAME" && -n "$TOKEN_ENV_VAR" ]] ||
			die "custom provider requires --module-path, --provider-name, and --token-env-var"
		[[ "$MODULE_PATH" =~ ^[A-Za-z0-9._~/-]+$ ]] || die "invalid custom module path"
		[[ "$PROVIDER_NAME" =~ ^[A-Za-z0-9_-]+$ ]] || die "invalid custom provider name"
		[[ "$TOKEN_ENV_VAR" =~ ^[A-Z_][A-Z0-9_]*$ ]] || die "invalid custom token environment variable"
		[[ -n "$MODULE_VERSION" ]] || die "custom provider requires --module-version"
		;;
	esac

if [[ -z "$WEBPORT_SOURCE" ]]; then
	if [[ "${WEBPORT_BOOTSTRAP:-0}" == 1 ]]; then
		WEBPORT_SOURCE=local
	else
		WEBPORT_SOURCE=build
	fi
fi
[[ "$WEBPORT_SOURCE" =~ ^(release|local|build)$ ]] || die "invalid webport source: $WEBPORT_SOURCE"

if [[ -z "$CADDY_SOURCE" ]]; then
	if [[ "$PROVIDER" == custom ]]; then
		CADDY_SOURCE=docker
	else
		CADDY_SOURCE=release
	fi
fi
[[ "$CADDY_SOURCE" =~ ^(release|docker|local|build)$ ]] || die "invalid Caddy source: $CADDY_SOURCE"

platform_os() { printf 'linux'; }

platform_arch() {
	case "$(uname -m)" in
		x86_64|amd64) printf 'amd64' ;;
		arm64|aarch64) printf 'arm64' ;;
		*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

download() {
	local url=$1 destination=$2
	if (( DRY_RUN )); then
		log "dry-run: curl -fsSL -o $destination $url"
	else
		curl -fsSL -o "$destination" "$url"
	fi
}

release_version() {
	if [[ -n "$VERSION" && "$VERSION" != latest ]]; then
		printf '%s' "$VERSION"
		return
	fi
	[[ -n "$RELEASE_BASE_URL" ]] && die "--version is required when --release-base-url is set"
	local repo=${WEBPORT_GITHUB_REPO:-webportdev/webport} json
	json=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest")
	printf '%s' "$json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

release_base_url() {
	if [[ -n "$RELEASE_BASE_URL" ]]; then
		printf '%s' "${RELEASE_BASE_URL%/}"
		return
	fi
	local repo=${WEBPORT_GITHUB_REPO:-webportdev/webport}
	printf 'https://github.com/%s/releases/latest/download' "$repo"
}

caddy_release_version() {
	printf '%s' "${CADDY_RELEASE_VERSION:-latest}"
}

caddy_release_base_url() {
	local version=$1
	if [[ -n "$CADDY_RELEASE_BASE_URL" ]]; then
		printf '%s' "${CADDY_RELEASE_BASE_URL%/}"
		return
	fi
	local repo=${WEBPORT_CADDY_GITHUB_REPO:-webportdev/webport-caddy}
	if [[ "$version" == latest ]]; then
		printf 'https://github.com/%s/releases/latest/download' "$repo"
		return
	fi
	printf 'https://github.com/%s/releases/download/%s' "$repo" "$version"
}

check_prerequisites() {
	local missing=() command
	[[ "$(uname -s)" == Linux ]] || die "only Linux is supported"
	for command in find install mkdir mktemp cp mv chmod grep systemctl tar; do
		command -v "$command" >/dev/null 2>&1 || missing+=("$command")
	done
	if [[ "$WEBPORT_SOURCE" == build || ( "$MODE" != webport && "$CADDY_SOURCE" == build ) ]]; then
		command -v go >/dev/null 2>&1 || missing+=("go")
	fi
	if [[ "$WEBPORT_SOURCE" == release || ( "$MODE" != webport && "$CADDY_SOURCE" == release ) ]]; then
		command -v curl >/dev/null 2>&1 || missing+=("curl")
	fi
	if [[ "$MODE" != webport && "$CADDY_SOURCE" == docker && "$DRY_RUN" == 0 ]]; then
		command -v docker >/dev/null 2>&1 || missing+=("docker")
	fi
	if [[ "$ROOT" == / ]]; then
		command -v sudo >/dev/null 2>&1 || missing+=("sudo")
	fi
	if [[ "$MODE" != webport ]]; then
		for command in getent id groupadd useradd chown; do
			command -v "$command" >/dev/null 2>&1 || missing+=("$command")
		done
	fi
	((${#missing[@]} == 0)) || die "missing required utilities: ${missing[*]}; install them with your OS package manager"
	if [[ "$ROOT" == / ]] && (( ! DRY_RUN )) && [[ ! -d /run/systemd/system ]]; then
		die "systemd is required and must be running"
	fi
}

validate_credentials_file() {
	local file=$1 line
	local reader=(cat)
	local grep_cmd=(grep)
	if [[ ! -r "$file" ]]; then
		if [[ "$ROOT" == / ]] && sudo test -r "$file"; then
			reader=(sudo cat)
			grep_cmd=(sudo grep)
		else
			die "credentials file is not readable: $file"
		fi
	fi
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ -z "$line" || "$line" == \#* || "$line" =~ ^[A-Z_][A-Z0-9_]*=.+$ ]] ||
			die "credentials file must contain EnvironmentFile-style KEY=value lines"
	done < <("${reader[@]}" "$file")
	if [[ "$PROVIDER" != route53 ]]; then
		"${grep_cmd[@]}" -q "^${TOKEN_ENV_VAR}=" "$file" || die "credentials file must define $TOKEN_ENV_VAR"
	else
		"${grep_cmd[@]}" -Eq '^AWS_(ACCESS_KEY_ID|PROFILE|WEB_IDENTITY_TOKEN_FILE)=' "$file" ||
			die "Route53 credentials file must define AWS credentials or an AWS profile"
	fi
}

collect_interactive_credentials() {
	local token secret access_key
	[[ -t 0 ]] || die "--credentials-file is required when stdin is not interactive"
	INTERACTIVE_CREDENTIALS=$(mktemp)
	chmod 600 "$INTERACTIVE_CREDENTIALS"
	if [[ "$PROVIDER" == route53 ]]; then
		read -r -p "AWS access key ID: " access_key
		read -r -s -p "AWS secret access key: " secret; printf '\n'
		printf 'AWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\n' "$access_key" "$secret" >"$INTERACTIVE_CREDENTIALS"
	else
		read -r -s -p "$TOKEN_ENV_VAR: " token; printf '\n'
		printf '%s=%s\n' "$TOKEN_ENV_VAR" "$token" >"$INTERACTIVE_CREDENTIALS"
	fi
	CREDENTIALS_FILE=$INTERACTIVE_CREDENTIALS
}

check_prerequisites
if [[ "$MODE" != webport || "$CONFIGURE_DNS" == 1 ]]; then
	if [[ -z "$CREDENTIALS_FILE" ]]; then
		(( NON_INTERACTIVE )) && die "--credentials-file is required for non-interactive caddy/full installs or DNS sync"
		collect_interactive_credentials
	fi
	validate_credentials_file "$CREDENTIALS_FILE"
fi
if [[ "$CONFIGURE_DNS" == 1 && "$PROVIDER" == custom ]]; then
	die "automatic DNS record management does not support custom providers"
fi
if [[ "$CONFIGURE_DNS" == 1 && -z "$DNS_IPV4" ]]; then
	die "DNS sync requires --dns-ipv4"
fi

if (( ! ASSUME_YES && ! NON_INTERACTIVE && ! DRY_RUN )); then
	read -r -p "Install mode '$MODE' with provider '$PROVIDER'? [y/N] " answer
	[[ "$answer" =~ ^[Yy]$ ]] || die "installation cancelled"
fi

managed_marker=$(path /etc/caddy/.webport-managed)
caddy_binary=$(path /usr/local/bin/caddy)
caddy_service=$(path /etc/systemd/system/caddy.service)

check_caddy_ownership() {
	local path_caddy=
	if [[ "$ROOT" == / ]]; then path_caddy=$(command -v caddy 2>/dev/null || true); fi
	if [[ ! -f "$managed_marker" && (
		-e "$caddy_binary" ||
		-e "$(path /usr/bin/caddy)" ||
		-e "$(path /usr/sbin/caddy)" ||
		-e "$caddy_service" ||
		-e "$(path /lib/systemd/system/caddy.service)" ||
		-e "$(path /usr/lib/systemd/system/caddy.service)" ||
		-n "$path_caddy"
	) ]]; then
		die "an unmanaged Caddy binary or service exists; remove or migrate it manually before using --mode $MODE"
	fi
}

existing_caddy() {
	if [[ -x "$caddy_binary" ]]; then printf '%s' "$caddy_binary"; return; fi
	if [[ -x "$(path /usr/bin/caddy)" ]]; then printf '%s' "$(path /usr/bin/caddy)"; return; fi
	if [[ "$ROOT" == / ]]; then command -v caddy 2>/dev/null || true; fi
}

verify_module() {
	local binary=$1
	"$binary" list-modules | grep -Fxq "dns.providers.$PROVIDER_NAME" ||
		die "Caddy does not contain dns.providers.$PROVIDER_NAME"
}

validate_caddyfile() {
	local binary=$1 config=$2
	if [[ -n "$TOKEN_ENV_VAR" ]]; then
		env "$TOKEN_ENV_VAR=0000000000000000000000000000000000000000" "$binary" validate --config "$config"
	else
		"$binary" validate --config "$config"
	fi
}

install_extracted_binary() {
	local root=$1 name=$2 destination=$3 source
	if [[ -x "$root/$name" ]]; then
		source="$root/$name"
	else
		source=$(find "$root" -type f -name "$name" -perm -111 | head -n 1)
	fi
	[[ -n "$source" ]] || die "artifact does not contain executable $name"
	privileged install -D -m 755 "$source" "$destination"
}

install_webport_from_archive() {
	local archive=$1 extract_dir="$BUILD_DIR/webport-artifact"
	run mkdir -p "$extract_dir"
	if (( DRY_RUN )); then
		log "dry-run: tar -xzf $archive -C $extract_dir"
		privileged install -D -m 755 "$extract_dir/webport" "$(path /usr/local/bin/webport)"
		privileged install -D -m 755 "$extract_dir/webportctl" "$(path /usr/local/bin/webportctl)"
		privileged install -D -m 755 "$extract_dir/webport-dns" "$(path /usr/local/bin/webport-dns)"
		return
	fi
	if (( ! DRY_RUN )); then
		tar -xzf "$archive" -C "$extract_dir"
	fi
	install_extracted_binary "$extract_dir" webport "$(path /usr/local/bin/webport)"
	install_extracted_binary "$extract_dir" webportctl "$(path /usr/local/bin/webportctl)"
	install_extracted_binary "$extract_dir" webport-dns "$(path /usr/local/bin/webport-dns)"
}

install_webport_binaries() {
	local os arch version base archive
	case "$WEBPORT_SOURCE" in
		build)
			run mkdir -p "$BUILD_DIR"
			run go build -o "$BUILD_DIR/webport" "$REPO_DIR/cmd/webport"
			run go build -o "$BUILD_DIR/webportctl" "$REPO_DIR/cmd/webportctl"
			run go build -o "$BUILD_DIR/webport-dns" "$REPO_DIR/cmd/webport-dns"
			privileged install -D -m 755 "$BUILD_DIR/webport" "$(path /usr/local/bin/webport)"
			privileged install -D -m 755 "$BUILD_DIR/webportctl" "$(path /usr/local/bin/webportctl)"
			privileged install -D -m 755 "$BUILD_DIR/webport-dns" "$(path /usr/local/bin/webport-dns)"
			;;
		local)
			[[ -n "$ARTIFACT_DIR" ]] || die "--artifact-dir is required with --webport-source local"
			if [[ -x "$ARTIFACT_DIR/webport" && -x "$ARTIFACT_DIR/webportctl" && -x "$ARTIFACT_DIR/webport-dns" ]]; then
				privileged install -D -m 755 "$ARTIFACT_DIR/webport" "$(path /usr/local/bin/webport)"
				privileged install -D -m 755 "$ARTIFACT_DIR/webportctl" "$(path /usr/local/bin/webportctl)"
				privileged install -D -m 755 "$ARTIFACT_DIR/webport-dns" "$(path /usr/local/bin/webport-dns)"
			else
				os=$(platform_os)
				arch=$(platform_arch)
				archive=$(find "$ARTIFACT_DIR" -maxdepth 1 -type f -name "webport_*_${os}_${arch}.tar.gz" | head -n 1)
				[[ -n "$archive" ]] || die "no webport artifact found in $ARTIFACT_DIR for $os/$arch"
				install_webport_from_archive "$archive"
			fi
			;;
		release)
			os=$(platform_os)
			arch=$(platform_arch)
			version=$(release_version)
			[[ -n "$version" ]] || die "could not resolve latest webport release version"
			base=$(release_base_url)
			archive="$BUILD_DIR/webport_${version}_${os}_${arch}.tar.gz"
			download "$base/$(basename "$archive")" "$archive"
			install_webport_from_archive "$archive"
			;;
	esac
}

install_webport() {
	local webport_env reload_binary=/usr/local/bin/caddy
	if [[ "$MODE" == webport ]] && (( ! DRY_RUN )); then
		local binary
		binary=$(existing_caddy)
		[[ -n "$binary" ]] || die "no existing Caddy binary found"
			verify_module "$binary"
			if [[ "$ROOT" == / ]]; then reload_binary=$binary; fi
		fi
	install_webport_binaries
	if [[ -n "$CREDENTIALS_FILE" ]]; then
		if [[ "$MODE" == full ]]; then
			DNS_CREDENTIALS_PATH=/etc/caddy/caddy.env
		else
			DNS_CREDENTIALS_PATH=/etc/webport/dns.env
			privileged install -D -m 600 "$CREDENTIALS_FILE" "$(path "$DNS_CREDENTIALS_PATH")"
		fi
	fi
	webport_env="WEBPORT_BASE_DOMAIN=$BASE_DOMAIN
WEBPORT_CADDYFILE_PATH=/etc/caddy/webport.d/Caddyfile
WEBPORT_CADDY_RELOAD_CMD=$reload_binary reload --config /etc/caddy/Caddyfile
WEBPORT_LISTEN_HOST=127.0.0.1
WEBPORT_TLS_DNS_PROVIDER=$PROVIDER
WEBPORT_TLS_DNS_PROVIDER_MODULE=$PROVIDER_NAME
WEBPORT_TLS_DNS_TOKEN_ENV_VAR=$TOKEN_ENV_VAR
WEBPORT_DNS_CREDENTIALS_FILE=$DNS_CREDENTIALS_PATH
WEBPORT_DNS_ZONE=$DNS_ZONE
"
	write_file "$(path /etc/webport/webport.env)" 600 "$webport_env"
	privileged install -D -m 644 "$REPO_DIR/systemd/webport.service" "$(path /etc/systemd/system/webport.service)"
	if [[ "$ROOT" == / ]]; then privileged chown root:root /etc/webport/webport.env; fi
}

sync_dns() {
	[[ "$CONFIGURE_DNS" == 1 ]] || return 0
	local args=(sync --config "$(path /etc/webport/webport.env)")
	[[ -z "$DNS_IPV4" ]] || args+=(--ipv4 "$DNS_IPV4")
	[[ -z "$DNS_IPV6" ]] || args+=(--ipv6 "$DNS_IPV6")
	[[ -z "$DNS_ZONE" ]] || args+=(--zone "$DNS_ZONE")
	privileged "$(path /usr/local/bin/webport-dns)" "${args[@]}"
}

local_caddy_candidate() {
	local os arch candidate
	[[ -n "$ARTIFACT_DIR" ]] || die "--artifact-dir is required with --caddy-source local"
	os=$(platform_os)
	arch=$(platform_arch)
	for candidate in \
		"$ARTIFACT_DIR/caddy" \
		"$ARTIFACT_DIR/caddy-$PROVIDER_NAME" \
		"$ARTIFACT_DIR/caddy-$PROVIDER_NAME-$os-$arch"; do
		if [[ -x "$candidate" ]]; then
			printf '%s' "$candidate"
			return
		fi
	done
	die "no local Caddy binary found in $ARTIFACT_DIR for $PROVIDER_NAME"
}

prepare_caddy_candidate() {
	local candidate=$1 validation_config directive args os arch version base asset
	case "$CADDY_SOURCE" in
		release)
			os=$(platform_os)
			arch=$(platform_arch)
			version=$(caddy_release_version)
			[[ -n "$version" ]] || die "could not resolve latest webport-caddy release version"
			base=$(caddy_release_base_url "$version")
			asset="caddy-$PROVIDER_NAME-$os-$arch"
			download "$base/$asset" "$candidate"
			run chmod +x "$candidate"
			;;
		build)
			run mkdir -p "$BUILD_DIR/bin"
			if (( ! DRY_RUN )); then
				GOBIN="$BUILD_DIR/bin" go install "github.com/caddyserver/xcaddy/cmd/xcaddy@$XCADDY_VERSION"
				"$BUILD_DIR/bin/xcaddy" build "$CADDY_VERSION" --with "$MODULE_PATH@$MODULE_VERSION" --output "$candidate"
			fi
			;;
		docker)
			args=(--provider "$PROVIDER" --caddy-version "$CADDY_VERSION" --module-version "$MODULE_VERSION" --output "$candidate")
			if [[ "$PROVIDER" == custom ]]; then
				args+=(--module-path "$MODULE_PATH" --provider-name "$PROVIDER_NAME" --token-env-var "$TOKEN_ENV_VAR")
			fi
			(( DRY_RUN )) && args+=(--dry-run)
			"$REPO_DIR/scripts/build-caddy-docker.sh" "${args[@]}"
			;;
		local)
			candidate=$(local_caddy_candidate)
			printf '%s' "$candidate" >"$BUILD_DIR/caddy.local"
			return
			;;
	esac
	if (( ! DRY_RUN )); then
		verify_module "$candidate"
		if [[ -n "$TOKEN_ENV_VAR" ]]; then
			directive="dns $PROVIDER_NAME {env.$TOKEN_ENV_VAR}"
		else
			directive="dns $PROVIDER_NAME"
		fi
		validation_config="$BUILD_DIR/Caddyfile"
		printf 'example.invalid {\n tls {\n  %s\n }\n}\n' "$directive" >"$validation_config"
		validate_caddyfile "$candidate" "$validation_config"
	fi
}

install_managed_caddy() {
	local candidate backup had_previous=0
	check_caddy_ownership
	candidate="$BUILD_DIR/caddy"
	prepare_caddy_candidate "$candidate"
	if [[ "$CADDY_SOURCE" == local ]]; then
		candidate=$(cat "$BUILD_DIR/caddy.local")
		if (( ! DRY_RUN )); then
			verify_module "$candidate"
			if [[ -n "$TOKEN_ENV_VAR" ]]; then
				directive="dns $PROVIDER_NAME {env.$TOKEN_ENV_VAR}"
			else
				directive="dns $PROVIDER_NAME"
			fi
			validation_config="$BUILD_DIR/Caddyfile"
			printf 'example.invalid {\n tls {\n  %s\n }\n}\n' "$directive" >"$validation_config"
			validate_caddyfile "$candidate" "$validation_config"
		fi
	fi

	privileged mkdir -p "$(path /etc/caddy/webport.d)" "$(path /var/lib/caddy)" "$(path /var/log/caddy)"
	ensure_file "$(path /etc/caddy/webport.d/Caddyfile)" 644
	if [[ "$ROOT" == / ]]; then
		getent group caddy >/dev/null || privileged groupadd --system caddy
		id -u caddy >/dev/null 2>&1 || privileged useradd --system --gid caddy --home-dir /var/lib/caddy --shell /usr/sbin/nologin caddy
	fi
	privileged install -D -m 644 "$REPO_DIR/caddy/Caddyfile" "$(path /etc/caddy/Caddyfile)"
	privileged install -D -m 644 "$REPO_DIR/systemd/caddy.service" "$caddy_service"
	privileged install -D -m 600 "$CREDENTIALS_FILE" "$(path /etc/caddy/caddy.env)"
	if [[ "$ROOT" == / ]]; then
		privileged chown root:root /etc/caddy/caddy.env
		privileged chown -R caddy:caddy /var/lib/caddy /var/log/caddy
	fi
	if (( ! DRY_RUN )) && [[ "$ROOT" == / ]]; then
		validate_caddyfile "$candidate" /etc/caddy/Caddyfile
	fi
	backup="$BUILD_DIR/caddy.previous"
	if [[ -e "$caddy_binary" ]] && (( ! DRY_RUN )); then
		cp "$caddy_binary" "$backup"
		had_previous=1
	fi
	privileged systemctl daemon-reload
	privileged systemctl stop caddy.service
	privileged install -D -m 755 "$candidate" "$caddy_binary"
	if (( ! DRY_RUN )) && ! privileged systemctl enable --now caddy.service; then
		if (( had_previous )); then privileged install -m 755 "$backup" "$caddy_binary"; fi
		privileged systemctl restart caddy.service || true
		die "managed Caddy failed to start; restored the previous managed binary"
	fi
	if (( DRY_RUN )); then
		privileged systemctl enable --now caddy.service
	fi
	write_file "$managed_marker" 644 "managed-by=webport
caddy-version=$CADDY_VERSION
module=$MODULE_PATH@$MODULE_VERSION
"
}

BUILD_DIR=$(mktemp -d)

case "$MODE" in
	webport) install_webport ;;
	caddy) install_managed_caddy ;;
	full) install_managed_caddy; install_webport ;;
esac

if [[ "$MODE" != caddy ]]; then
	sync_dns
	privileged systemctl daemon-reload
	privileged systemctl enable --now webport.service
fi
log "Installation complete."
