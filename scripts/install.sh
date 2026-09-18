#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_DIR=$(cd -- "$SCRIPT_DIR/.." && pwd)
# shellcheck source=versions.env
source "$SCRIPT_DIR/versions.env"

MODE=
TLS_MODE=
PROVIDER=
BASE_DOMAIN=
CREDENTIALS_FILE=
WEBPORT_SOURCE=${WEBPORT_SOURCE:-}
TRAEFIK_SOURCE=${TRAEFIK_SOURCE:-release}
VERSION=${WEBPORT_VERSION:-}
RELEASE_BASE_URL=${WEBPORT_RELEASE_BASE_URL:-}
TRAEFIK_RELEASE_BASE_URL=${WEBPORT_TRAEFIK_RELEASE_BASE_URL:-}
ARTIFACT_DIR=${WEBPORT_ARTIFACT_DIR:-}
DNS_IPV4=
DNS_IPV6=
DNS_ZONE=
CONFIGURE_DNS=0
TRUST_LOCAL_CA=0
NON_INTERACTIVE=0
ASSUME_YES=0
DRY_RUN=0
UPGRADE=0
AI_SKILL_ONLY=0
ROOT=${WEBPORT_INSTALL_ROOT:-/}
PLATFORM=${WEBPORT_INSTALL_PLATFORM:-linux}
[[ "$PLATFORM" =~ ^(linux|darwin)$ ]] || {
	printf 'error: unsupported install platform: %s\n' "$PLATFORM" >&2
	exit 1
}
BUILD_DIR=
INTERACTIVE_CREDENTIALS=

cleanup() {
	[[ -z "$BUILD_DIR" ]] || rm -rf "$BUILD_DIR"
	[[ -z "$INTERACTIVE_CREDENTIALS" ]] || rm -f "$INTERACTIVE_CREDENTIALS"
}
trap cleanup EXIT

usage() {
	cat <<'EOF'
Usage: scripts/install.sh [options]

Modes:
  --mode webport|traefik|full
  --external-traefik         Alias for --mode webport
Proxy TLS:
  --tls-mode acme|local-ca
  --public | --local         Select public ACME or local-CA mode
  --trust-local-ca           Trust the generated CA in the OS trust store
  --provider LEGO_PROVIDER_CODE
  --credentials-file FILE
Required for webport/full:
  --base-domain DOMAIN
Binary sources:
  --webport-source release|local|build
  --traefik-source release|local
  --version VERSION --release-base-url URL --artifact-dir DIR
  --traefik-version VERSION --traefik-release-base-url URL
Automation:
  --dns-ipv4 ADDRESS --dns-ipv6 ADDRESS --dns-zone ZONE
  --non-interactive --yes --dry-run

Agent skills:
  --ai-skill                 Install the Webport skill for AI coding agents

Upgrade:
  --upgrade                 Reuse the existing installation configuration

Cloudflare uses CF_DNS_API_TOKEN. DigitalOcean uses DO_AUTH_TOKEN.
Other Lego providers require a credentials file containing their environment variables.
With no mode/TLS/domain options, installs full local HTTPS at webport.localhost.
EOF
}

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*"; }
path() { printf '%s%s' "${ROOT%/}" "$1"; }

run() {
	if (( DRY_RUN )); then
		printf 'dry-run:'; printf ' %q' "$@"; printf '\n'
	else
		"$@"
	fi
}

privileged() {
	if (( DRY_RUN )); then
		printf 'dry-run:'; [[ "$ROOT" == / ]] && printf ' sudo'; printf ' %q' "$@"; printf '\n'
	elif [[ "$ROOT" == / ]]; then
		sudo "$@"
	else
		"$@"
	fi
}

write_file() {
	local destination=$1 mode=$2 content=$3 tmp
	if (( DRY_RUN )); then
		log "dry-run: write $destination (mode $mode)"
		return
	fi
	tmp=$(mktemp)
	printf '%s' "$content" >"$tmp"
	privileged mkdir -p "$(dirname "$destination")"
	privileged install -m "$mode" "$tmp" "$destination"
	rm -f "$tmp"
}

install_file() {
	local source=$1 destination=$2 mode=$3
	privileged mkdir -p "$(dirname "$destination")"
	privileged install -m "$mode" "$source" "$destination"
}

install_ai_skill() {
	local skill_name=webport-development source destination answer
	local destinations
	source="$REPO_DIR/skills/$skill_name/SKILL.md"
	[[ -n "${HOME:-}" && "$HOME" != / ]] || die "HOME must point to a user home directory"
	[[ -r "$source" ]] || die "agent skill asset is unavailable: $source"
	destinations=(
		"$HOME/.codex/skills/$skill_name"
		"$HOME/.config/opencode/skills/$skill_name"
		"$HOME/.pi/agent/skills/$skill_name"
		"$HOME/.claude/skills/$skill_name"
	)
	for destination in "${destinations[@]}"; do
		if (( DRY_RUN )); then
			log "dry-run: install agent skill at $destination/SKILL.md"
			continue
		fi
		if [[ -e "$destination/SKILL.md" || -L "$destination/SKILL.md" ]]; then
			if (( ! ASSUME_YES )); then
				(( NON_INTERACTIVE )) && die "agent skill already exists at $destination/SKILL.md; rerun with --yes"
				[[ -t 0 ]] || die "replacing an agent skill requires an interactive stdin or --yes"
				read -r -p "Replace $destination/SKILL.md? [y/N] " answer
				[[ "$answer" =~ ^[Yy]$ ]] || { log "Skipped $destination/SKILL.md"; continue; }
			fi
		fi
		mkdir -p "$destination"
		install -m 0644 "$source" "$destination/SKILL.md"
		log "Installed agent skill at $destination/SKILL.md"
	done
}

while (($#)); do
	case "$1" in
		--mode) MODE=${2:-}; shift 2 ;;
		--tls-mode) TLS_MODE=${2:-}; shift 2 ;;
		--public) TLS_MODE=acme; shift ;;
		--local) TLS_MODE=local-ca; shift ;;
		--external-traefik) MODE=webport; shift ;;
		--provider) PROVIDER=${2:-}; shift 2 ;;
		--base-domain) BASE_DOMAIN=${2:-}; shift 2 ;;
		--credentials-file) CREDENTIALS_FILE=${2:-}; shift 2 ;;
		--webport-source) WEBPORT_SOURCE=${2:-}; shift 2 ;;
		--traefik-source) TRAEFIK_SOURCE=${2:-}; shift 2 ;;
		--version) VERSION=${2:-}; shift 2 ;;
		--release-base-url) RELEASE_BASE_URL=${2:-}; shift 2 ;;
		--traefik-version) TRAEFIK_VERSION=${2:-}; shift 2 ;;
		--traefik-release-base-url) TRAEFIK_RELEASE_BASE_URL=${2:-}; shift 2 ;;
		--artifact-dir) ARTIFACT_DIR=${2:-}; shift 2 ;;
		--dns-ipv4) DNS_IPV4=${2:-}; CONFIGURE_DNS=1; shift 2 ;;
		--dns-ipv6) DNS_IPV6=${2:-}; CONFIGURE_DNS=1; shift 2 ;;
		--dns-zone) DNS_ZONE=${2:-}; shift 2 ;;
		--trust-local-ca) TRUST_LOCAL_CA=1; shift ;;
		--non-interactive) NON_INTERACTIVE=1; shift ;;
		--yes|-y) ASSUME_YES=1; shift ;;
		--dry-run) DRY_RUN=1; shift ;;
		--upgrade) UPGRADE=1; shift ;;
		--ai-skill) AI_SKILL_ONLY=1; shift ;;
		--help|-h) usage; exit 0 ;;
		--caddy-*|--module-*|--provider-name|--token-env-var) die "Caddy options were removed; use Traefik options" ;;
		--*token*|--*secret*|--*credential-value*) die "secret values must be supplied through --credentials-file" ;;
		*) die "unknown argument: $1" ;;
	esac
done

if (( AI_SKILL_ONLY )); then
	install_ai_skill
	exit 0
fi

if (( EUID == 0 )) && [[ "$ROOT" == / ]]; then
	printf 'error: do not run this installer as root; rerun it without sudo\n' >&2
	exit 1
fi

prompt_value() {
	local variable=$1 prompt=$2 value
	[[ -t 0 ]] || die "$variable is required (stdin is not interactive)"
	read -r -p "$prompt: " value
	printf -v "$variable" '%s' "$value"
}

managed_caddy_marker=
managed_traefik_marker=
caddy_binary=
traefik_binary=
caddy_service=
traefik_service=
webport_service=
webport_stack_target=
traefik_config_path=
traefik_dynamic_path=
traefik_credentials_path=
dns_credentials_path=
webport_config_path=
local_ca_path=
if [[ "$PLATFORM" == darwin ]]; then
	managed_caddy_marker=$(path /usr/local/etc/caddy/.webport-managed)
	managed_traefik_marker=$(path /usr/local/etc/traefik/.webport-managed)
	caddy_binary=$(path /usr/local/bin/caddy)
	traefik_binary=$(path /usr/local/bin/traefik)
	caddy_service=$(path /Library/LaunchDaemons/com.webport.caddy.plist)
	traefik_service=$(path /Library/LaunchDaemons/com.webport.traefik.plist)
	webport_service=$(path /Library/LaunchDaemons/com.webport.webport.plist)
	traefik_config_path=/usr/local/etc/traefik/traefik.yml
	traefik_dynamic_path=/usr/local/etc/traefik/dynamic/webport.yml
	traefik_credentials_path=/usr/local/etc/traefik/traefik.env
	dns_credentials_path=/usr/local/etc/webport/dns.env
	webport_config_path=/usr/local/etc/webport/webport.env
	local_ca_path=/usr/local/etc/traefik/dynamic/webport-pki
else
	managed_caddy_marker=$(path /etc/caddy/.webport-managed)
	managed_traefik_marker=$(path /etc/traefik/.webport-managed)
	caddy_binary=$(path /usr/local/bin/caddy)
	traefik_binary=$(path /usr/local/bin/traefik)
	caddy_service=$(path /etc/systemd/system/caddy.service)
	traefik_service=$(path /etc/systemd/system/traefik.service)
	webport_service=$(path /etc/systemd/system/webport.service)
	webport_stack_target=$(path /etc/systemd/system/webport-stack.target)
	traefik_config_path=/etc/traefik/traefik.yml
	traefik_dynamic_path=/etc/traefik/dynamic/webport.yml
	traefik_credentials_path=/etc/traefik/traefik.env
	dns_credentials_path=/etc/webport/dns.env
	webport_config_path=/etc/webport/webport.env
	local_ca_path=/etc/traefik/dynamic/webport-pki
fi

read_config_value() {
	local key=$1 file=$2 line=
	if [[ -r "$file" ]]; then
		line=$(grep -E "^${key}=" "$file" | tail -n 1 || true)
	elif [[ "$ROOT" == / ]]; then
		command -v sudo >/dev/null 2>&1 || die "sudo is required to read the existing webport configuration"
		line=$(sudo grep -E "^${key}=" "$file" | tail -n 1 || true)
	else
		return 1
	fi
	[[ -n "$line" ]] || return 1
	printf '%s' "${line#*=}"
}

map_installed_path() {
	local value=$1
	if [[ "$ROOT" != / && "$value" == /* ]]; then
		path "$value"
	else
		printf '%s' "$value"
	fi
}

load_upgrade_configuration() {
	local config_file existing_base existing_tls existing_provider existing_zone existing_credentials
	config_file=$(path "$webport_config_path")
	[[ -f "$config_file" ]] ||
		die "cannot upgrade: existing webport configuration was not found at $webport_config_path"
	existing_base=$(read_config_value WEBPORT_BASE_DOMAIN "$config_file") ||
		die "cannot upgrade: WEBPORT_BASE_DOMAIN is missing from $webport_config_path"
	existing_tls=$(read_config_value WEBPORT_TLS_MODE "$config_file") ||
		die "cannot upgrade: WEBPORT_TLS_MODE is missing from $webport_config_path"
	existing_provider=$(read_config_value WEBPORT_DNS_PROVIDER "$config_file") || true
	existing_zone=$(read_config_value WEBPORT_DNS_ZONE "$config_file") || true
	existing_credentials=$(read_config_value WEBPORT_DNS_CREDENTIALS_FILE "$config_file") || true

	[[ -n "$MODE" ]] || {
		if [[ -f "$managed_traefik_marker" ]]; then MODE=full; else MODE=webport; fi
	}
	[[ -n "$BASE_DOMAIN" ]] || BASE_DOMAIN=$existing_base
	[[ -n "$TLS_MODE" ]] || TLS_MODE=$existing_tls
	[[ -n "$PROVIDER" ]] || PROVIDER=$existing_provider
	[[ -n "$DNS_ZONE" ]] || DNS_ZONE=$existing_zone
	[[ -n "$CREDENTIALS_FILE" ]] || CREDENTIALS_FILE=$(map_installed_path "$existing_credentials")
}

if (( UPGRADE )); then
	load_upgrade_configuration
fi

[[ -n "$MODE" ]] || MODE=full
if [[ -z "$TLS_MODE" ]]; then
	[[ -n "$PROVIDER" ]] && TLS_MODE=acme || TLS_MODE=local-ca
fi
if [[ "$TLS_MODE" == local-ca && -z "$BASE_DOMAIN" && "$MODE" != traefik ]]; then
	BASE_DOMAIN=webport.localhost
fi
[[ "$MODE" =~ ^(webport|traefik|full)$ ]] || die "invalid mode: $MODE"
[[ "$TLS_MODE" =~ ^(acme|local-ca)$ ]] || die "invalid TLS mode: $TLS_MODE"
[[ "$TRAEFIK_SOURCE" =~ ^(release|local)$ ]] || die "invalid Traefik source: $TRAEFIK_SOURCE"
[[ -z "$PROVIDER" || "$PROVIDER" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || die "invalid Lego provider code: $PROVIDER"
[[ -z "$BASE_DOMAIN" || "$BASE_DOMAIN" =~ ^[A-Za-z0-9.-]+$ ]] || die "invalid base domain: $BASE_DOMAIN"
[[ -z "$DNS_ZONE" || "$DNS_ZONE" =~ ^[A-Za-z0-9.-]+$ ]] || die "invalid DNS zone: $DNS_ZONE"
if (( TRUST_LOCAL_CA )); then
	[[ "$TLS_MODE" == local-ca ]] || die "--trust-local-ca requires --tls-mode local-ca"
	[[ "$MODE" != traefik ]] || die "--trust-local-ca requires webport or full install mode"
fi
if (( ! UPGRADE )) && [[ "$TLS_MODE" == local-ca && "$MODE" != traefik && "$ROOT" == / ]]; then
	if (( NON_INTERACTIVE && ! TRUST_LOCAL_CA )); then
		die "non-interactive local installation requires --trust-local-ca"
	fi
	if (( ! NON_INTERACTIVE && ! TRUST_LOCAL_CA )); then
		read -r -p "Trust the webport local CA in this machine's system trust store? [Y/n] " answer
		[[ ! "$answer" =~ ^[Nn]$ ]] && TRUST_LOCAL_CA=1
	fi
fi

if [[ ( "$MODE" != traefik || "$TLS_MODE" == local-ca ) && -z "$BASE_DOMAIN" ]]; then
	(( NON_INTERACTIVE )) && die "--base-domain is required"
	prompt_value BASE_DOMAIN "Base domain"
fi
if [[ "$TLS_MODE" == acme && "$MODE" != webport && -z "$PROVIDER" ]]; then
	(( NON_INTERACTIVE )) && die "--provider is required"
	prompt_value PROVIDER "Traefik Lego DNS provider code"
fi
if (( CONFIGURE_DNS )) && [[ -z "$PROVIDER" ]]; then
	die "--provider is required with DNS synchronization"
fi

if [[ -z "$WEBPORT_SOURCE" ]]; then
	if [[ "${WEBPORT_BOOTSTRAP:-0}" == 1 ]]; then
		WEBPORT_SOURCE=local
	elif [[ -d "$REPO_DIR/cmd/webport" ]]; then
		WEBPORT_SOURCE=build
	else
		WEBPORT_SOURCE=release
	fi
fi
[[ "$WEBPORT_SOURCE" =~ ^(release|local|build)$ ]] || die "invalid webport source: $WEBPORT_SOURCE"

check_prerequisites() {
	local missing=() command
	for command in awk cp find grep install mkdir mktemp rm sed tr; do
		command -v "$command" >/dev/null 2>&1 || missing+=("$command")
	done
	if [[ "$WEBPORT_SOURCE" == build && "$MODE" != traefik ]]; then
		command -v go >/dev/null 2>&1 || missing+=("go")
	fi
	if [[ "$MODE" != webport && "$TRAEFIK_SOURCE" == release ]]; then
		for command in curl tar; do command -v "$command" >/dev/null 2>&1 || missing+=("$command"); done
		if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
			missing+=("sha256sum-or-shasum")
		fi
	fi
	if [[ "$MODE" != webport ]]; then
		command -v curl >/dev/null 2>&1 || missing+=("curl")
	fi
	if [[ "$PLATFORM" == darwin ]]; then
		command -v launchctl >/dev/null 2>&1 || missing+=("launchctl")
		if [[ "$ROOT" == / ]]; then
			for command in dscl seq; do command -v "$command" >/dev/null 2>&1 || missing+=("$command"); done
		fi
		# lsof is used by the installed daemon, not while staging an
		# installation beneath WEBPORT_INSTALL_ROOT. Avoid requiring the
		# macOS host path in cross-platform installer tests.
		if [[ "$ROOT" == / ]]; then
			[[ -x /usr/sbin/lsof ]] || missing+=("/usr/sbin/lsof")
		fi
		if (( TRUST_LOCAL_CA )); then
			for command in security cmp; do command -v "$command" >/dev/null 2>&1 || missing+=("$command"); done
		fi
	else
		command -v systemctl >/dev/null 2>&1 || missing+=("systemctl")
	fi
	if [[ "$ROOT" == / ]]; then
		command -v sudo >/dev/null 2>&1 || missing+=("sudo")
	fi
	((${#missing[@]} == 0)) || die "missing required utilities: ${missing[*]}"
}
check_prerequisites

platform_os() { printf '%s' "$PLATFORM"; }
platform_arch() {
	case "$(uname -m)" in
		x86_64|amd64) printf 'amd64' ;;
		arm64|aarch64) printf 'arm64' ;;
		*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

download() {
	local url=$1 destination=$2
	if (( DRY_RUN )); then log "dry-run: curl -fsSL -o $destination $url"; else curl -fsSL -o "$destination" "$url"; fi
}

sha256_verify() {
	local sums=$1 file=$2 expected actual
	expected=$(grep -E "[[:space:]]\\*?$(basename "$file")$" "$sums" | awk '{print $1}' | head -n 1)
	[[ -n "$expected" ]] || die "checksum missing for $(basename "$file")"
	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$file" | awk '{print $1}')
	else
		actual=$(shasum -a 256 "$file" | awk '{print $1}')
	fi
	[[ "$actual" == "$expected" ]] || die "checksum mismatch for $(basename "$file")"
}

validate_credentials_file() {
	local file=$1 line assignments=0 value trimmed
	[[ -r "$file" ]] || die "credentials file is not readable: $file"
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ -z "$line" || "$line" == \#* ]] && continue
		[[ "$line" =~ ^[A-Z_][A-Z0-9_]*=.+$ ]] || die "credentials file must contain KEY=value lines"
		assignments=$((assignments + 1))
	done <"$file"
	(( assignments > 0 )) || die "credentials file contains no assignments"
	case "$PROVIDER" in
		cloudflare)
			grep -q '^CF_DNS_API_TOKEN=' "$file" || die "Cloudflare credentials must define CF_DNS_API_TOKEN"
			value=$(sed -n 's/^CF_DNS_API_TOKEN=//p' "$file" | head -n 1)
			[[ ! "$value" =~ ^[Bb]earer[[:space:]] ]] ||
				die "CF_DNS_API_TOKEN must contain only the raw token, without a Bearer prefix"
			case "$value" in
				\"*|*\"|\'*|*\') die "CF_DNS_API_TOKEN must not include surrounding quotes" ;;
			esac
			trimmed=$(printf '%s' "$value" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
			[[ "$value" == "$trimmed" ]] || die "CF_DNS_API_TOKEN must not include surrounding whitespace"
			;;
		digitalocean) grep -q '^DO_AUTH_TOKEN=' "$file" || die "DigitalOcean credentials must define DO_AUTH_TOKEN" ;;
		route53) grep -Eq '^AWS_(ACCESS_KEY_ID|PROFILE|WEB_IDENTITY_TOKEN_FILE)=' "$file" || die "Route53 credentials must define AWS credentials or a profile" ;;
	esac
}

collect_interactive_credentials() {
	local token secret access_key
	[[ -t 0 ]] || die "--credentials-file is required"
	INTERACTIVE_CREDENTIALS=$(mktemp)
	chmod 600 "$INTERACTIVE_CREDENTIALS"
	case "$PROVIDER" in
		cloudflare)
			read -r -s -p "CF_DNS_API_TOKEN: " token; printf '\n'
			printf 'CF_DNS_API_TOKEN=%s\n' "$token" >"$INTERACTIVE_CREDENTIALS"
			;;
		digitalocean)
			read -r -s -p "DO_AUTH_TOKEN: " token; printf '\n'
			printf 'DO_AUTH_TOKEN=%s\n' "$token" >"$INTERACTIVE_CREDENTIALS"
			;;
		route53)
			read -r -p "AWS access key ID: " access_key
			read -r -s -p "AWS secret access key: " secret; printf '\n'
			printf 'AWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\n' "$access_key" "$secret" >"$INTERACTIVE_CREDENTIALS"
			;;
		*) die "--credentials-file is required for provider $PROVIDER" ;;
	esac
	CREDENTIALS_FILE=$INTERACTIVE_CREDENTIALS
}

migrating_caddy=0
stack_available=0

if [[ "$MODE" != webport ]]; then
	if [[ -f "$managed_caddy_marker" ]]; then
		migrating_caddy=1
		local_caddy_credentials=$(path /etc/caddy/caddy.env)
		[[ "$PLATFORM" == darwin ]] && local_caddy_credentials=$(path /usr/local/etc/caddy/caddy.env)
		if [[ -z "$CREDENTIALS_FILE" && -f "$local_caddy_credentials" ]]; then
			CREDENTIALS_FILE=$local_caddy_credentials
		fi
	elif [[ -e "$caddy_binary" || -e "$caddy_service" ]]; then
		die "an unmanaged Caddy installation exists and conflicts with Traefik on ports 80/443"
	fi
	if [[ ! -f "$managed_traefik_marker" && ( -e "$traefik_binary" || -e "$traefik_service" ) ]]; then
		die "an unmanaged Traefik installation exists; use --mode webport or remove it manually"
	fi
fi

normalize_migrated_credentials() {
	local source=$1 destination=$2 readable_source=$1
	if [[ ! -r "$source" ]]; then
		[[ "$ROOT" == / ]] || die "credentials file is not readable: $source"
		readable_source="$BUILD_DIR/source-credentials.env"
		sudo cat "$source" >"$readable_source"
		chmod 600 "$readable_source"
	fi
	if (( migrating_caddy )) && [[ "$PROVIDER" == cloudflare ]] &&
		grep -q '^CLOUDFLARE_API_TOKEN=' "$readable_source" &&
		! grep -q '^CF_DNS_API_TOKEN=' "$readable_source"; then
		sed 's/^CLOUDFLARE_API_TOKEN=/CF_DNS_API_TOKEN=/' "$readable_source" >"$destination"
	else
		cp "$readable_source" "$destination"
	fi
	chmod 600 "$destination"
	CREDENTIALS_FILE=$destination
}

if [[ ( "$TLS_MODE" == acme && "$MODE" != webport ) || "$CONFIGURE_DNS" == 1 ]]; then
	[[ -n "$CREDENTIALS_FILE" ]] || { (( NON_INTERACTIVE )) && die "--credentials-file is required"; collect_interactive_credentials; }
	BUILD_DIR=$(mktemp -d)
	normalize_migrated_credentials "$CREDENTIALS_FILE" "$BUILD_DIR/traefik.env"
	validate_credentials_file "$CREDENTIALS_FILE"
fi
if (( CONFIGURE_DNS )) && [[ ! "$PROVIDER" =~ ^(cloudflare|digitalocean|route53)$ ]]; then
	die "wildcard DNS synchronization supports only cloudflare, digitalocean, and route53"
fi
if (( CONFIGURE_DNS )) && [[ -z "$DNS_IPV4" ]]; then
	die "DNS synchronization requires --dns-ipv4"
fi
[[ -n "$BUILD_DIR" ]] || BUILD_DIR=$(mktemp -d)

if (( ! ASSUME_YES && ! NON_INTERACTIVE && ! DRY_RUN )); then
	read -r -p "Install mode '$MODE' with TLS mode '$TLS_MODE' and Traefik provider '${PROVIDER:-none}'? [y/N] " answer
	[[ "$answer" =~ ^[Yy]$ ]] || die "installation cancelled"
fi

release_version() {
	if [[ -n "$VERSION" && "$VERSION" != latest ]]; then printf '%s' "$VERSION"; return; fi
	[[ -n "$RELEASE_BASE_URL" ]] && die "--version is required with --release-base-url"
	local repo=${WEBPORT_GITHUB_REPO:-webportdev/webport} json
	json=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest")
	printf '%s' "$json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

webport_release_base() {
	if [[ -n "$RELEASE_BASE_URL" ]]; then printf '%s' "${RELEASE_BASE_URL%/}"; else printf 'https://github.com/%s/releases/latest/download' "${WEBPORT_GITHUB_REPO:-webportdev/webport}"; fi
}

install_extracted_binary() {
	local root=$1 name=$2 destination=$3 source=
	source=$(find "$root" -type f -name "$name" -perm -u+x | head -n 1)
	[[ -n "$source" ]] || die "artifact does not contain executable $name"
	install_file "$source" "$destination" 755
}

install_webport_binaries() {
	local os arch version base archive extract
	case "$WEBPORT_SOURCE" in
		build)
			run go build -o "$BUILD_DIR/webport" "$REPO_DIR/cmd/webport"
			run go build -o "$BUILD_DIR/webportctl" "$REPO_DIR/cmd/webportctl"
			run go build -o "$BUILD_DIR/webport-dns" "$REPO_DIR/cmd/webport-dns"
			for name in webport webportctl webport-dns; do install_file "$BUILD_DIR/$name" "$(path /usr/local/bin/$name)" 755; done
			;;
		local)
			[[ -n "$ARTIFACT_DIR" ]] || die "--artifact-dir is required with --webport-source local"
			if [[ -x "$ARTIFACT_DIR/webport" && -x "$ARTIFACT_DIR/webportctl" && -x "$ARTIFACT_DIR/webport-dns" ]]; then
				for name in webport webportctl webport-dns; do
					install_file "$ARTIFACT_DIR/$name" "$(path /usr/local/bin/$name)" 755
				done
			else
				os=$(platform_os); arch=$(platform_arch)
				archive=$(find "$ARTIFACT_DIR" -maxdepth 1 -type f -name "webport_*_${os}_${arch}.tar.gz" | head -n 1)
				[[ -n "$archive" ]] || die "no webport artifact found for $os/$arch"
				extract="$BUILD_DIR/webport-artifact"; mkdir -p "$extract"; tar -xzf "$archive" -C "$extract"
				for name in webport webportctl webport-dns; do install_extracted_binary "$extract" "$name" "$(path /usr/local/bin/$name)"; done
			fi
			;;
		release)
			os=$(platform_os); arch=$(platform_arch); version=$(release_version); base=$(webport_release_base)
			archive="$BUILD_DIR/webport_${version}_${os}_${arch}.tar.gz"
			download "$base/$(basename "$archive")" "$archive"
			extract="$BUILD_DIR/webport-artifact"; run mkdir -p "$extract"
			if (( ! DRY_RUN )); then tar -xzf "$archive" -C "$extract"; fi
			for name in webport webportctl webport-dns; do install_extracted_binary "$extract" "$name" "$(path /usr/local/bin/$name)"; done
			;;
	esac
}

prepare_traefik_binary() {
	local os arch archive sums base extract
	case "$TRAEFIK_SOURCE" in
		local)
			[[ -n "$ARTIFACT_DIR" && -x "$ARTIFACT_DIR/traefik" ]] || die "--artifact-dir must contain executable traefik"
			printf '%s' "$ARTIFACT_DIR/traefik"
			;;
		release)
			os=$(platform_os); arch=$(platform_arch)
			base=${TRAEFIK_RELEASE_BASE_URL:-https://github.com/traefik/traefik/releases/download/$TRAEFIK_VERSION}
			archive="$BUILD_DIR/traefik_${TRAEFIK_VERSION}_${os}_${arch}.tar.gz"
			sums="$BUILD_DIR/traefik_${TRAEFIK_VERSION}_checksums.txt"
			download "${base%/}/$(basename "$archive")" "$archive"
			download "${base%/}/$(basename "$sums")" "$sums"
			if (( DRY_RUN )); then printf '%s' "$BUILD_DIR/traefik"; return; fi
			sha256_verify "$sums" "$archive"
			extract="$BUILD_DIR/traefik-artifact"; mkdir -p "$extract"; tar -xzf "$archive" -C "$extract"
			[[ -x "$extract/traefik" ]] || die "Traefik archive does not contain an executable"
			printf '%s' "$extract/traefik"
			;;
	esac
}

render_traefik_config() {
	local output=$1
	local template=$REPO_DIR/traefik/traefik.yml.tmpl
	[[ "$PLATFORM" == darwin ]] && template=$REPO_DIR/macos/traefik.yml.tmpl
	if [[ "$TLS_MODE" == local-ca ]]; then
		awk '
			/^certificatesResolvers:/ { skipping=1; next }
			skipping && /^ping:/ { skipping=0 }
			!skipping { print }
		' "$template" >"$output"
	else
		sed "s/__WEBPORT_DNS_PROVIDER__/$PROVIDER/g" "$template" >"$output"
	fi
}

remove_managed_caddy() {
	privileged rm -f "$caddy_binary" "$caddy_service"
	if [[ "$PLATFORM" == darwin ]]; then
		privileged rm -f "$(path /usr/local/libexec/webport/run-caddy)"
		privileged rm -rf "$(path /usr/local/etc/caddy)" "$(path /usr/local/var/lib/caddy)" "$(path /usr/local/var/log/caddy)"
	else
		privileged rm -rf "$(path /etc/caddy)" "$(path /var/lib/caddy)" "$(path /var/log/caddy)"
	fi
}

daemon_reload() {
	[[ "$PLATFORM" == darwin ]] || privileged systemctl daemon-reload
}

stop_caddy() {
	if [[ "$PLATFORM" == darwin ]]; then
		(( DRY_RUN )) || privileged launchctl bootout system/com.webport.caddy >/dev/null 2>&1 || true
	else
		privileged systemctl stop caddy.service
	fi
}

start_caddy() {
	if [[ "$PLATFORM" == darwin ]]; then
		privileged launchctl bootstrap system "$caddy_service" || true
		privileged launchctl kickstart -k system/com.webport.caddy || true
	else
		privileged systemctl start caddy.service || true
	fi
}

start_traefik() {
	if [[ "$PLATFORM" == darwin ]]; then
		(( DRY_RUN )) || privileged launchctl bootout system/com.webport.traefik >/dev/null 2>&1 || true
		privileged launchctl bootstrap system "$traefik_service" &&
			privileged launchctl kickstart -k system/com.webport.traefik
	else
		if [[ "$MODE" == traefik && "$stack_available" == 0 ]]; then
			privileged systemctl enable traefik.service
		fi
		privileged systemctl restart traefik.service
	fi
}

stop_traefik() {
	if [[ "$PLATFORM" == darwin ]]; then
		(( DRY_RUN )) || privileged launchctl bootout system/com.webport.traefik >/dev/null 2>&1 || true
	else
		privileged systemctl stop traefik.service || true
	fi
}

backup_managed_traefik() {
	local backup_dir=$1
	[[ -f "$managed_traefik_marker" ]] || return 1
	mkdir -p "$backup_dir"
	backup_file "$traefik_binary" "$backup_dir/traefik" || return 1
	backup_file "$(path "$traefik_config_path")" "$backup_dir/traefik.yml" || return 1
	backup_file "$(path "$traefik_credentials_path")" "$backup_dir/traefik.env" || return 1
	backup_file "$traefik_service" "$backup_dir/service" || return 1
	backup_file "$managed_traefik_marker" "$backup_dir/marker" || return 1
	if [[ "$PLATFORM" == darwin ]]; then
		backup_file "$(path /usr/local/libexec/webport/run-traefik)" "$backup_dir/run-traefik" || return 1
	fi
	return 0
}

backup_file() {
	local source=$1 destination=$2
	if [[ "$ROOT" == / ]]; then
		privileged cat "$source" >"$destination"
	elif [[ -r "$source" ]]; then
		cp "$source" "$destination"
	else
		return 1
	fi
}

rollback_traefik() {
	local backup_dir=${1:-}
	stop_traefik
	if [[ -n "$backup_dir" && -d "$backup_dir" ]]; then
		privileged install -m 755 "$backup_dir/traefik" "$traefik_binary"
		privileged install -m 644 "$backup_dir/traefik.yml" "$(path "$traefik_config_path")"
		privileged install -m 600 "$backup_dir/traefik.env" "$(path "$traefik_credentials_path")"
		privileged install -m 644 "$backup_dir/service" "$traefik_service"
		privileged install -m 644 "$backup_dir/marker" "$managed_traefik_marker"
		if [[ "$PLATFORM" == darwin ]]; then
			privileged install -m 755 "$backup_dir/run-traefik" "$(path /usr/local/libexec/webport/run-traefik)"
		fi
		daemon_reload
		start_traefik || true
	else
		privileged rm -f "$traefik_binary" "$traefik_service" "$managed_traefik_marker"
		if [[ "$PLATFORM" == darwin ]]; then
			privileged rm -f "$(path /usr/local/libexec/webport/run-traefik)"
			privileged rm -rf "$(path /usr/local/etc/traefik)" "$(path /usr/local/var/lib/traefik)" "$(path /usr/local/var/log/traefik)"
		else
			privileged rm -rf "$(path /etc/traefik)" "$(path /var/lib/traefik)" "$(path /var/log/traefik)"
		fi
		daemon_reload
	fi
	if (( migrating_caddy )); then start_caddy; fi
}

disable_caddy() {
	if [[ "$PLATFORM" == darwin ]]; then
		:
	else
		privileged systemctl disable caddy.service || true
	fi
}

start_webport() {
	if [[ "$PLATFORM" == darwin ]]; then
		(( DRY_RUN )) || privileged launchctl bootout system/com.webport.webport >/dev/null 2>&1 || true
		privileged launchctl bootstrap system "$webport_service"
		privileged launchctl kickstart -k system/com.webport.webport
	else
		privileged systemctl restart webport.service
	fi
}

enable_webport_stack() {
	[[ "$PLATFORM" == linux && "$stack_available" == 1 ]] || return 0
	privileged systemctl enable --now webport-stack.target
	log "Manage the stack with: sudo systemctl {start|stop|restart|status} webport-stack.target"
}

migrate_existing_webport() {
	local installed_webport old_env filtered content
	installed_webport=$(path /usr/local/bin/webport)
	old_env=$(path "$webport_config_path")
	[[ -x "$installed_webport" && -f "$old_env" ]] || return 0
	filtered="$BUILD_DIR/webport.env.migrated"
	if [[ -r "$old_env" ]]; then
		grep -Ev '^(WEBPORT_CADDY|WEBPORT_TLS_DNS|WEBPORT_TLS_MODE=|WEBPORT_LOCAL_CA_DIR=|WEBPORT_TRAEFIK_|WEBPORT_DNS_PROVIDER=|WEBPORT_DNS_CREDENTIALS_FILE=)' "$old_env" >"$filtered" || true
	else
		sudo grep -Ev '^(WEBPORT_CADDY|WEBPORT_TLS_DNS|WEBPORT_TLS_MODE=|WEBPORT_LOCAL_CA_DIR=|WEBPORT_TRAEFIK_|WEBPORT_DNS_PROVIDER=|WEBPORT_DNS_CREDENTIALS_FILE=)' "$old_env" >"$filtered" || true
	fi
	content="$(<"$filtered")
WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH=$traefik_dynamic_path
WEBPORT_TRAEFIK_ENTRYPOINT=websecure
WEBPORT_TRAEFIK_CERT_RESOLVER=webport
WEBPORT_TLS_MODE=$TLS_MODE
WEBPORT_LOCAL_CA_DIR=$local_ca_path
WEBPORT_DNS_PROVIDER=$PROVIDER
WEBPORT_DNS_CREDENTIALS_FILE=$traefik_credentials_path
"
	write_file "$old_env" 600 "$content"
	if [[ "$PLATFORM" == darwin ]]; then
		install_file "$REPO_DIR/macos/run-webport" "$(path /usr/local/libexec/webport/run-webport)" 755
		install_file "$REPO_DIR/macos/com.webport.webport.plist" "$webport_service" 644
	else
		install_file "$REPO_DIR/systemd/webport.service" "$webport_service" 644
		install_file "$REPO_DIR/systemd/webport-stack.target" "$webport_stack_target" 644
		stack_available=1
	fi
	daemon_reload
	start_webport
}

install_managed_traefik() {
	local candidate config backup_dir=
	candidate=$(prepare_traefik_binary)
	config="$BUILD_DIR/traefik.yml"; render_traefik_config "$config"
	if [[ "$DRY_RUN" == 0 && -f "$managed_traefik_marker" ]]; then
		backup_managed_traefik "$BUILD_DIR/traefik-previous" ||
			die "could not back up the existing managed Traefik installation"
		backup_dir=$BUILD_DIR/traefik-previous
	fi
	if [[ "$ROOT" == / && "$PLATFORM" == linux ]]; then
		getent group traefik >/dev/null || privileged groupadd --system traefik
		id -u traefik >/dev/null 2>&1 || privileged useradd --system --gid traefik --home-dir /var/lib/traefik --shell /usr/sbin/nologin traefik
	fi
	if [[ "$PLATFORM" == darwin ]]; then
		privileged mkdir -p "$(path /usr/local/etc/traefik/dynamic)" "$(path /usr/local/var/lib/traefik)" "$(path /usr/local/var/log/traefik)"
		[[ -e "$(path /usr/local/var/lib/traefik/acme.json)" ]] || write_file "$(path /usr/local/var/lib/traefik/acme.json)" 600 ""
		install_file "$REPO_DIR/macos/run-traefik" "$(path /usr/local/libexec/webport/run-traefik)" 755
		install_file "$REPO_DIR/macos/com.webport.traefik.plist" "$traefik_service" 644
	else
		privileged mkdir -p "$(path /etc/traefik/dynamic)" "$(path /var/lib/traefik)" "$(path /var/log/traefik)"
		[[ -e "$(path /var/lib/traefik/acme.json)" ]] || write_file "$(path /var/lib/traefik/acme.json)" 600 ""
		install_file "$REPO_DIR/systemd/traefik.service" "$traefik_service" 644
	fi
	install_file "$config" "$(path "$traefik_config_path")" 644
	if [[ -n "$CREDENTIALS_FILE" ]]; then
		install_file "$CREDENTIALS_FILE" "$(path "$traefik_credentials_path")" 600
	else
		write_file "$(path "$traefik_credentials_path")" 600 ""
	fi
	install_file "$candidate" "$traefik_binary" 755
	write_file "$managed_traefik_marker" 644 "managed-by=webport
traefik-version=$TRAEFIK_VERSION
provider=$PROVIDER
"
	if [[ "$ROOT" == / && "$PLATFORM" == linux && "$DRY_RUN" == 0 ]]; then privileged chown -R traefik:traefik /var/lib/traefik /var/log/traefik; fi
	daemon_reload
	if (( migrating_caddy )); then stop_caddy; fi
	if ! start_traefik; then
		rollback_traefik "$backup_dir"
		die "managed Traefik failed to start; restored the previous proxy"
	fi
	if [[ "$ROOT" == / && "$DRY_RUN" == 0 ]]; then
		local healthy=0
		for _ in {1..20}; do
			if curl --noproxy '*' -fsS http://127.0.0.1:8082/ping >/dev/null 2>&1; then healthy=1; break; fi
			sleep 0.25
		done
		if (( ! healthy )); then
			rollback_traefik "$backup_dir"
			die "managed Traefik health check failed; restored the previous proxy"
		fi
	fi
	if (( migrating_caddy )); then
		disable_caddy
		remove_managed_caddy
		daemon_reload
	fi
	if [[ "$MODE" == traefik ]]; then migrate_existing_webport; fi
}

install_webport() {
	local env_content credentials_path=
	if [[ "$MODE" == webport && "$DRY_RUN" == 0 ]]; then
		[[ -x "$traefik_binary" ]] || die "no existing Traefik binary found at /usr/local/bin/traefik"
		"$traefik_binary" version >/dev/null || die "existing Traefik binary is not usable"
	fi
	install_webport_binaries
	local installer_assets
	installer_assets=$(path /usr/local/libexec/webport/installer)
	install_file "$REPO_DIR/scripts/install.sh" "$installer_assets/scripts/install.sh" 755
	install_file "$REPO_DIR/scripts/install-macos.sh" "$installer_assets/scripts/install-macos.sh" 755
	install_file "$REPO_DIR/scripts/bootstrap-install.sh" "$installer_assets/scripts/bootstrap-install.sh" 755
	install_file "$REPO_DIR/scripts/versions.env" "$installer_assets/scripts/versions.env" 644
	install_file "$REPO_DIR/systemd/webport.service" "$installer_assets/systemd/webport.service" 644
	install_file "$REPO_DIR/systemd/traefik.service" "$installer_assets/systemd/traefik.service" 644
	install_file "$REPO_DIR/systemd/webport-stack.target" "$installer_assets/systemd/webport-stack.target" 644
	install_file "$REPO_DIR/systemd/webport.env.example" "$installer_assets/systemd/webport.env.example" 644
	install_file "$REPO_DIR/traefik/traefik.yml.tmpl" "$installer_assets/traefik/traefik.yml.tmpl" 644
	install_file "$REPO_DIR/macos/traefik.yml.tmpl" "$installer_assets/macos/traefik.yml.tmpl" 644
	install_file "$REPO_DIR/macos/run-traefik" "$installer_assets/macos/run-traefik" 755
	install_file "$REPO_DIR/macos/run-webport" "$installer_assets/macos/run-webport" 755
	install_file "$REPO_DIR/macos/com.webport.traefik.plist" "$installer_assets/macos/com.webport.traefik.plist" 644
	install_file "$REPO_DIR/macos/com.webport.webport.plist" "$installer_assets/macos/com.webport.webport.plist" 644
	install_file "$REPO_DIR/skills/webport-development/SKILL.md" "$installer_assets/skills/webport-development/SKILL.md" 644
	if [[ "$PLATFORM" == darwin ]]; then
		if [[ "$ROOT" == / && "$DRY_RUN" == 0 ]] && ! id -u _webport >/dev/null 2>&1; then
			local service_uid=
			for candidate_uid in $(seq 499 -1 400); do
				if ! dscl . -search /Users UniqueID "$candidate_uid" | grep -q .; then
					service_uid=$candidate_uid
					break
				fi
			done
			[[ -n "$service_uid" ]] || die "could not allocate a macOS system UID for _webport"
			privileged dscl . -create /Users/_webport
			privileged dscl . -create /Users/_webport UserShell /usr/bin/false
			privileged dscl . -create /Users/_webport RealName "Webport service"
			privileged dscl . -create /Users/_webport UniqueID "$service_uid"
			privileged dscl . -create /Users/_webport PrimaryGroupID 20
			privileged dscl . -create /Users/_webport NFSHomeDirectory /var/empty
		fi
		install_file "$REPO_DIR/macos/run-webport" "$(path /usr/local/libexec/webport/run-webport)" 755
		install_file "$REPO_DIR/macos/com.webport.webport.plist" "$webport_service" 644
	else
		if [[ "$ROOT" == / && "$DRY_RUN" == 0 ]]; then
			getent group webport >/dev/null || privileged groupadd --system webport
			id -u webport >/dev/null 2>&1 ||
				privileged useradd --system --gid webport --home-dir /var/lib/webport --shell /usr/sbin/nologin webport
		fi
		install_file "$REPO_DIR/systemd/webport.service" "$webport_service" 644
		install_file "$REPO_DIR/systemd/webport-stack.target" "$webport_stack_target" 644
		stack_available=1
	fi
	if [[ -n "$CREDENTIALS_FILE" ]]; then
		if [[ "$MODE" == webport ]]; then
			install_file "$CREDENTIALS_FILE" "$(path "$dns_credentials_path")" 600
			credentials_path=$dns_credentials_path
		else
			credentials_path=$traefik_credentials_path
		fi
	fi
	if [[ "$TLS_MODE" == local-ca ]]; then
		privileged mkdir -p "$(path "$local_ca_path")"
		if [[ "$ROOT" == / && "$PLATFORM" == linux && "$DRY_RUN" == 0 ]] && getent group traefik >/dev/null; then
			privileged chown webport:traefik "$local_ca_path"
			privileged chmod 2750 "$local_ca_path"
		elif [[ "$ROOT" == / && "$PLATFORM" == darwin && "$DRY_RUN" == 0 ]]; then
			privileged chown _webport:staff "$local_ca_path"
			privileged chmod 0750 "$local_ca_path"
		fi
	fi
	env_content="WEBPORT_BASE_DOMAIN=$BASE_DOMAIN
WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH=$traefik_dynamic_path
WEBPORT_TRAEFIK_ENTRYPOINT=websecure
WEBPORT_TRAEFIK_CERT_RESOLVER=webport
WEBPORT_TLS_MODE=$TLS_MODE
WEBPORT_LOCAL_CA_DIR=$local_ca_path
WEBPORT_LISTEN_HOST=127.0.0.1
WEBPORT_PORT=8080
WEBPORT_DEFAULT_TTL=30s
WEBPORT_TTL_CHECK_INTERVAL=10s
WEBPORT_SHUTDOWN_TIMEOUT=5s
WEBPORT_DISCOVERY_ENABLED=false
WEBPORT_DISCOVERY_INTERVAL=2s
WEBPORT_DNS_PROVIDER=$PROVIDER
WEBPORT_DNS_CREDENTIALS_FILE=$credentials_path
WEBPORT_DNS_ZONE=$DNS_ZONE
"
	write_file "$(path "$webport_config_path")" 600 "$env_content"
	privileged mkdir -p "$(dirname "$(path "$traefik_dynamic_path")")"
	if [[ "$ROOT" == / && "$DRY_RUN" == 0 ]]; then
		if [[ "$PLATFORM" == linux ]]; then
			privileged chown webport:traefik "$(dirname "$traefik_dynamic_path")"
			privileged chmod 2750 "$(dirname "$traefik_dynamic_path")"
		else
			privileged chown _webport:staff "$(dirname "$traefik_dynamic_path")"
			privileged chmod 0750 "$(dirname "$traefik_dynamic_path")"
			privileged chown root:staff "$webport_config_path"
			privileged chmod 0640 "$webport_config_path"
			privileged mkdir -p /usr/local/var/log/webport
			privileged chown _webport:staff /usr/local/var/log/webport
		fi
	fi
	daemon_reload
	start_webport
}

sync_dns() {
	(( CONFIGURE_DNS )) || return 0
	local args=(sync --config "$(path "$webport_config_path")" --ipv4 "$DNS_IPV4")
	[[ -z "$DNS_IPV6" ]] || args+=(--ipv6 "$DNS_IPV6")
	[[ -z "$DNS_ZONE" ]] || args+=(--zone "$DNS_ZONE")
	privileged "$(path /usr/local/bin/webport-dns)" "${args[@]}"
}

trust_local_ca() {
	(( TRUST_LOCAL_CA )) || return 0
	local ca_file existing label
	ca_file=$(path "$local_ca_path/ca.crt")
	label="webport local development CA for $(printf '%s' "$BASE_DOMAIN" | tr '[:upper:]' '[:lower:]')"
	existing="$BUILD_DIR/keychain-ca.crt"

	if (( DRY_RUN )); then
		log "dry-run: wait for $ca_file"
		if [[ "$PLATFORM" == darwin ]]; then
			privileged security add-trusted-cert -d -r trustRoot \
				-k /Library/Keychains/System.keychain "$ca_file"
		else
			log "dry-run: trust $ca_file in the Linux system trust store"
		fi
		return
	fi

	for _ in {1..40}; do
		[[ -s "$ca_file" ]] && break
		sleep 0.25
	done
	[[ -s "$ca_file" ]] || die "local CA was not generated at $ca_file; inspect webport logs"

	if [[ "$PLATFORM" == linux ]]; then
		if command -v update-ca-certificates >/dev/null 2>&1; then
			privileged install -m 0644 "$ca_file" /usr/local/share/ca-certificates/webport-local-ca.crt
			privileged update-ca-certificates
		elif command -v update-ca-trust >/dev/null 2>&1; then
			privileged install -m 0644 "$ca_file" /etc/pki/ca-trust/source/anchors/webport-local-ca.crt
			privileged update-ca-trust extract
		elif command -v trust >/dev/null 2>&1; then
			privileged trust anchor --store "$ca_file"
		else
			die "cannot automate CA trust: install update-ca-certificates or p11-kit, or trust $ca_file manually"
		fi
		log "Trusted the webport local CA in the Linux system trust store."
		return
	fi

	if security find-certificate -c "$label" -p \
		/Library/Keychains/System.keychain >"$existing" 2>/dev/null; then
		if cmp -s "$ca_file" "$existing"; then
			log "Local CA is already trusted in the macOS System Keychain."
			return
		fi
		privileged security delete-certificate -c "$label" \
			/Library/Keychains/System.keychain
	fi
	privileged security add-trusted-cert -d -r trustRoot \
		-k /Library/Keychains/System.keychain "$ca_file"
	log "Trusted the webport local CA in the macOS System Keychain."
}

wait_for_webport_ready() {
	local api=http://127.0.0.1:8080
	for _ in {1..40}; do
		if curl --noproxy '*' -fsS "$api/ready" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.25
	done
	return 1
}

case "$MODE" in
	traefik) install_managed_traefik ;;
	webport) install_webport; sync_dns ;;
	full) install_managed_traefik; install_webport; sync_dns ;;
esac
enable_webport_stack
trust_local_ca

if [[ "$ROOT" == / && "$DRY_RUN" == 0 && "$TLS_MODE" == local-ca ]]; then
	if ! wait_for_webport_ready; then
		die "installation completed but the webport daemon did not become ready; inspect webport service logs"
	fi
	if (( TRUST_LOCAL_CA )) && ! "$(path /usr/local/bin/webport)" doctor; then
		die "installation completed but post-install diagnostics failed; run 'webport doctor' after resolving the reported issue"
	fi
	if (( ! TRUST_LOCAL_CA )); then
		log "Skipped TLS trust diagnostics because the local CA was not trusted."
	fi
fi

log "Installation complete."
