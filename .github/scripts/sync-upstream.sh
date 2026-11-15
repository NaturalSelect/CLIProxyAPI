#!/usr/bin/env bash
# Cherry-picks new upstream commits onto the current branch, one upstream
# first-parent step at a time, and records progress in the marker file.
set -euo pipefail

upstream_url="${UPSTREAM_URL:-https://github.com/router-for-me/CLIProxyAPI.git}"
upstream_branch="${UPSTREAM_BRANCH:-main}"
marker_file="${UPSTREAM_MARKER_FILE:-.github/upstream-sync-base}"
output_file="${GITHUB_OUTPUT:-/dev/null}"
report_file="${SYNC_REPORT_FILE:-sync-report.md}"

emit() { printf '%s=%s\n' "$1" "$2" >> "$output_file"; }

if ! git remote get-url upstream >/dev/null 2>&1; then
	git remote add upstream "$upstream_url"
fi
git fetch --no-tags upstream "$upstream_branch"
target="$(git rev-parse "refs/remotes/upstream/$upstream_branch")"

base="$(tr -d '[:space:]' < "$marker_file")"
if ! git cat-file -e "${base}^{commit}" 2>/dev/null; then
	printf '::error::Marker %s points to unknown commit %s\n' "$marker_file" "$base"
	exit 1
fi

# NOTE: Walking the first-parent chain keeps the marker meaningful: every commit before it is an ancestor.
mapfile -t steps < <(git rev-list --reverse --first-parent "$base..$target")
emit upstream_head "$target"
emit pending "${#steps[@]}"
if [[ ${#steps[@]} -eq 0 ]]; then
	printf 'Already in sync with upstream %s.\n' "${target:0:9}"
	emit status up-to-date
	exit 0
fi

applied=0
skipped=0
last="$base"
blocked=""
for commit in "${steps[@]}"; do
	pick_args=(-x)
	if [[ "$(git rev-list --parents -n 1 "$commit" | wc -w)" -gt 2 ]]; then
		pick_args+=(-m 1)
	fi
	if git cherry-pick "${pick_args[@]}" "$commit" >/dev/null 2>&1; then
		applied=$((applied + 1))
		last="$commit"
		continue
	fi
	if [[ -z "$(git diff --name-only --diff-filter=U)" ]] && git diff --cached --quiet; then
		# NOTE: The change is already present in the fork, so the pick is empty.
		git cherry-pick --skip
		skipped=$((skipped + 1))
		last="$commit"
		continue
	fi
	blocked="$commit"
	conflicts="$(git diff --name-only --diff-filter=U)"
	git cherry-pick --abort
	break
done

if [[ "$last" != "$base" ]]; then
	printf '%s\n' "$last" > "$marker_file"
	git add "$marker_file"
	git commit -m "chore: record upstream sync point ${last:0:9}" >/dev/null
fi

{
	printf '## Upstream sync\n\n'
	printf '| | |\n|---|---|\n'
	printf '| Upstream head | `%s` |\n' "${target:0:9}"
	printf '| Previous sync point | `%s` |\n' "${base:0:9}"
	printf '| New sync point | `%s` |\n' "${last:0:9}"
	printf '| Upstream steps pending | %s |\n' "${#steps[@]}"
	printf '| Applied | %s |\n' "$applied"
	printf '| Skipped as already present | %s |\n' "$skipped"
	if [[ -n "$blocked" ]]; then
		printf '\n### Blocked by a conflict\n\n'
		printf 'Upstream commit `%s` does not apply cleanly:\n\n' "$blocked"
		printf '> %s\n\n' "$(git log -1 --format=%s "$blocked")"
		printf 'Conflicting files:\n\n'
		while IFS= read -r file; do
			[[ -n "$file" ]] && printf -- '- `%s`\n' "$file"
		done <<< "$conflicts"
		printf '\nTo unblock, on an up-to-date `main`:\n\n'
		printf '```bash\n'
		printf 'git fetch %s %s\n' "$upstream_url" "$upstream_branch"
		if [[ "$(git rev-list --parents -n 1 "$blocked" | wc -w)" -gt 2 ]]; then
			printf 'git cherry-pick -x -m 1 %s\n' "$blocked"
		else
			printf 'git cherry-pick -x %s\n' "$blocked"
		fi
		printf '# resolve conflicts, then: git cherry-pick --continue\n'
		printf 'echo %s > %s\n' "$blocked" "$marker_file"
		printf 'git commit -am "chore: record upstream sync point %s"\n' "${blocked:0:9}"
		printf '```\n'
	fi
} > "$report_file"

emit applied "$applied"
if [[ "$last" != "$base" ]]; then
	emit advanced true
else
	emit advanced false
fi
emit blocked "$blocked"
if [[ -n "$blocked" ]]; then
	emit status blocked
else
	emit status synced
fi
cat "$report_file"
