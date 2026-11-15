#!/usr/bin/env bash
# NOTE: Only fork-changed files are checked; upstream files may be formatted by a newer gofmt than CI's toolchain.
set -euo pipefail

upstream_url="${UPSTREAM_URL:-https://github.com/router-for-me/CLIProxyAPI.git}"
marker_file="${UPSTREAM_MARKER_FILE:-.github/upstream-sync-base}"

base="$(tr -d '[:space:]' < "$marker_file")"
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
	git fetch --no-tags --depth=1 "$upstream_url" "$base"
fi

mapfile -t files < <(git diff --name-only --no-renames --diff-filter=AM "$base" HEAD -- '*.go')
if [[ ${#files[@]} -eq 0 ]]; then
	exit 0
fi
unformatted="$(gofmt -l "${files[@]}")"
if [[ -n "$unformatted" ]]; then
	printf 'The following Go files require gofmt:\n%s\n' "$unformatted"
	exit 1
fi
