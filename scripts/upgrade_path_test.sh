#!/usr/bin/env bash

# Shared regression coverage for both platform installers. The caller supplies
# INSTALLER, artifacts, root (an existing fake installation), tmp, and fail.
(
	path_home="$tmp/path upgrade home"
	path_bin="$path_home/.local/bin"
	mkdir -p "$path_bin"
	old_binary=$(command -v bash)
	for name in webport webportctl webport-dns; do
		cp "$old_binary" "$path_bin/$name"
	done

	upgrade_path() {
		HOME="$path_home" PATH="$path_bin:$PATH" WEBPORT_INSTALL_ROOT="$root" \
			"$INSTALLER" --upgrade --mode webport --webport-source local \
			--artifact-dir "$artifacts" --non-interactive --yes "$@"
	}

	upgrade_path --dry-run >"$tmp/path-upgrade-dry-run.log"
	for name in webport webportctl webport-dns; do
		cmp -s "$old_binary" "$path_bin/$name" || fail "dry-run replaced $name on PATH"
		assert_contains "$tmp/path-upgrade-dry-run.log" "refresh $name on PATH at $path_bin/$name"
	done

	# The command being upgraded must remain runnable while its file is replaced.
	"$path_bin/webport" -c 'while :; do sleep 1; done' &
	path_cli_pid=$!
	trap 'kill "$path_cli_pid" 2>/dev/null || true; wait "$path_cli_pid" 2>/dev/null || true' EXIT
	upgrade_path >"$tmp/path-upgrade.log"
	kill -0 "$path_cli_pid" || fail "PATH upgrade interrupted the old CLI"
	for name in webport webportctl webport-dns; do
		cmp -s "$artifacts/$name" "$path_bin/$name" || fail "upgrade left stale $name on PATH"
		assert_contains "$tmp/path-upgrade.log" "Refreshed $name on PATH at $path_bin/$name"
	done

	# Managed symlinks and script wrappers must keep their original meaning.
	rm "$path_bin/webport" "$path_bin/webportctl" "$path_bin/webport-dns"
	ln -s "$root/usr/local/bin/webport" "$path_bin/webport"
	ln -s "$old_binary" "$path_bin/webportctl"
	printf '#!/bin/sh\nexit 42\n' >"$path_bin/webport-dns"
	chmod +x "$path_bin/webport-dns"
	upgrade_path >"$tmp/path-upgrade-preserved.log"
	[[ -L "$path_bin/webport" && -L "$path_bin/webportctl" ]] || fail "upgrade replaced a symlink"
	assert_contains "$path_bin/webport-dns" 'exit 42'
	assert_contains "$tmp/path-upgrade-preserved.log" "Warning: $path_bin/webportctl shadows"
	assert_contains "$tmp/path-upgrade-preserved.log" "Warning: $path_bin/webport-dns shadows"
	if grep -Fq "Warning: $path_bin/webport shadows" "$tmp/path-upgrade-preserved.log"; then
		fail "upgrade warned about a link to the upgraded binary"
	fi

	# Missing user copies stay absent; shadowing files outside HOME are reported.
	rm "$path_bin/webportctl" "$path_bin/webport-dns"
	rm "$path_bin/webport"
	cp "$old_binary" "$path_bin/webport"
	HOME="$tmp/other-home" PATH="$path_bin:$PATH" WEBPORT_INSTALL_ROOT="$root" \
		"$INSTALLER" --upgrade --mode webport --webport-source local \
		--artifact-dir "$artifacts" --non-interactive --yes >"$tmp/path-upgrade-outside-home.log"
	cmp -s "$old_binary" "$path_bin/webport" || fail "upgrade replaced a copy outside HOME"
	assert_contains "$tmp/path-upgrade-outside-home.log" "Warning: $path_bin/webport shadows"
	[[ ! -e "$path_bin/webportctl" && ! -e "$path_bin/webport-dns" ]] || fail "upgrade created an absent PATH copy"
)
