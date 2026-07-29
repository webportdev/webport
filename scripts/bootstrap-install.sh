#!/usr/bin/env bash
set -euo pipefail

REPO=${WEBPORT_GITHUB_REPO:-webportdev/webport}
VERSION=${WEBPORT_VERSION:-latest}
RELEASE_BASE_URL=${WEBPORT_RELEASE_BASE_URL:-}
tmp=

cleanup() {
	[[ -z "$tmp" ]] || rm -rf "$tmp"
}
trap cleanup EXIT

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
log() { printf '%s\n' "$*"; }

platform_os() {
	case "$(uname -s)" in
		Linux) printf 'linux' ;;
		Darwin) printf 'darwin' ;;
		*) die "unsupported OS: $(uname -s)" ;;
	esac
}

platform_arch() {
	case "$(uname -m)" in
		x86_64|amd64) printf 'amd64' ;;
		arm64|aarch64) printf 'arm64' ;;
		*) die "unsupported architecture: $(uname -m)" ;;
	esac
}

resolve_version() {
	if [[ -n "$VERSION" && "$VERSION" != latest ]]; then
		printf '%s' "$VERSION"
		return
	fi
	local json
	json=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest")
	printf '%s' "$json" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1
}

base_url() {
	if [[ -n "$RELEASE_BASE_URL" ]]; then
		printf '%s' "${RELEASE_BASE_URL%/}"
	else
		printf 'https://github.com/%s/releases/latest/download' "$REPO"
	fi
}

download() {
	local url=$1 destination=$2
	log "Downloading $url"
	curl -fsSL -o "$destination" "$url"
}

sha256_verify() {
	local sums=$1 file=$2 expected actual name
	name=$(basename "$file")
	expected=$(grep -E "[[:space:]]\*?${name}$|[[:space:]]${name}$" "$sums" | awk '{print $1}' | head -n 1)
	[[ -n "$expected" ]] || die "checksum missing for $name"
	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$file" | awk '{print $1}')
	else
		actual=$(shasum -a 256 "$file" | awk '{print $1}')
	fi
	[[ "$actual" == "$expected" ]] || die "checksum mismatch for $name"
}

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
	die "sha256sum or shasum is required"
fi

resolved_version=$(resolve_version)
[[ -n "$resolved_version" ]] || die "could not resolve latest release version"
os=$(platform_os)
arch=$(platform_arch)
base=$(base_url)
tmp=$(mktemp -d)
artifacts="$tmp/artifacts"
installer_root="$tmp/installer"
mkdir -p "$artifacts" "$installer_root"

installer_archive="webport-installer_${resolved_version}.tar.gz"
webport_archive="webport_${resolved_version}_${os}_${arch}.tar.gz"

download "$base/SHA256SUMS" "$tmp/SHA256SUMS"
download "$base/$installer_archive" "$tmp/$installer_archive"
download "$base/$webport_archive" "$artifacts/$webport_archive"
sha256_verify "$tmp/SHA256SUMS" "$tmp/$installer_archive"
sha256_verify "$tmp/SHA256SUMS" "$artifacts/$webport_archive"

tar -xzf "$tmp/$installer_archive" -C "$installer_root"
if [[ -x "$installer_root/scripts/install.sh" || -x "$installer_root/scripts/install-macos.sh" ]]; then
	repo_root=$installer_root
else
	installer_path=$(find "$installer_root" -mindepth 1 -maxdepth 3 -type f \
		-path '*/scripts/install.sh' -print -quit)
	[[ -n "$installer_path" ]] || die "installer bundle does not contain install scripts"
	repo_root=$(cd -- "$(dirname "$installer_path")/.." && pwd)
fi

case "$os" in
	linux) installer="$repo_root/scripts/install.sh" ;;
	darwin) installer="$repo_root/scripts/install-macos.sh" ;;
esac

run_installer() {
	WEBPORT_BOOTSTRAP=1 WEBPORT_ARTIFACT_DIR="$artifacts" "$installer" \
		--webport-source local \
		--artifact-dir "$artifacts" \
		--version "$resolved_version" \
		--release-base-url "$base" \
		"$@"
}

# `bash -s` consumes the curl pipe as stdin. Reattach the installer to the
# controlling terminal when one exists so its interactive prompts still work.
if { exec 3</dev/tty; } 2>/dev/null; then
	run_installer "$@" <&3
	exec 3<&-
else
	run_installer "$@"
fi
