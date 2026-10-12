#!/usr/bin/env bash
# release.sh: the one place the Go and Python release versions are read, checked and tagged.
#
# The Go module and the Python package version independently:
#   - Go: the newest released "## [X.Y.Z]" heading in CHANGELOG.md, tagged "vX.Y.Z".
#   - Python: the version in python/techai_webutils/pyproject.toml, tagged
#     "techai-webutils-vX.Y.Z" and published to PyPI.
#
# Usage:
#   scripts/release.sh check     # CI on a PR: fail if either version is unreleasable
#   scripts/release.sh release   # the release workflow on main: tag and publish what is new
#
# Both commands need the full history and tags (actions/checkout with fetch-depth: 0).
set -euo pipefail

readonly PY_DIR="python/techai_webutils"
readonly PY_TAG_PREFIX="techai-webutils-v"
readonly PYPI_URL="https://pypi.org/pypi/techai-webutils/json"

# go_version prints the newest released version in CHANGELOG.md (the first "## [X.Y.Z]" heading;
# "## [Unreleased]" is skipped).
go_version() {
	sed -n 's/^## \[\([0-9][0-9.]*\)\].*/\1/p' CHANGELOG.md | head -n 1
}

# py_version prints the Python package version from its pyproject.
py_version() {
	sed -n 's/^version = "\(.*\)"$/\1/p' "$PY_DIR/pyproject.toml" | head -n 1
}

# newest prints the highest of the versions on stdin (one per line), or nothing.
newest() {
	sort -V | tail -n 1
}

# newer reports whether version $1 is strictly greater than version $2 (an empty $2 is older).
newer() {
	[[ -z "$2" ]] && return 0
	[[ "$1" != "$2" && "$(printf '%s\n%s\n' "$1" "$2" | newest)" == "$1" ]]
}

# tag_exists reports whether the tag $1 exists.
tag_exists() {
	git rev-parse -q --verify "refs/tags/$1" >/dev/null
}

# pypi_versions prints every techai-webutils version on PyPI, one per line.
pypi_versions() {
	curl -fsS "$PYPI_URL" | python3 -c 'import json, sys; print("\n".join(json.load(sys.stdin)["releases"]))'
}

# fail prints its arguments as an error and exits.
fail() {
	echo "release: $*" >&2
	exit 1
}

# check_go fails when the CHANGELOG's released version is untagged yet not newer than every
# Go tag, so a merge can never re-release or go backwards.
check_go() {
	local version latest
	version="$(go_version)"
	[[ -n "$version" ]] || fail "CHANGELOG.md has no released \"## [X.Y.Z]\" section"
	if tag_exists "v$version"; then
		echo "Go: v$version is already released; nothing to tag."
		return
	fi
	latest="$(git tag -l 'v[0-9]*' | sed 's/^v//' | newest)"
	newer "$version" "$latest" || fail "Go: CHANGELOG version $version is not newer than v$latest"
	echo "Go: v$version will be tagged on merge."
}

# check_py fails when the package changed since its last release without a version bump, or
# when an unreleased version is not newer than every tagged and every PyPI version.
check_py() {
	local version latest
	version="$(py_version)"
	[[ -n "$version" ]] || fail "Python: no version in $PY_DIR/pyproject.toml"
	if tag_exists "$PY_TAG_PREFIX$version"; then
		if ! git diff --quiet "$PY_TAG_PREFIX$version" HEAD -- "$PY_DIR/src" "$PY_DIR/pyproject.toml"; then
			fail "Python: the package changed since $PY_TAG_PREFIX$version; bump the version in $PY_DIR/pyproject.toml"
		fi
		echo "Python: $version is already released and the package is unchanged."
		return
	fi
	latest="$({ git tag -l "$PY_TAG_PREFIX*" | sed "s/^$PY_TAG_PREFIX//"; pypi_versions; } | newest)"
	newer "$version" "$latest" || fail "Python: version $version is not newer than $latest (tags and PyPI)"
	echo "Python: $version will be published and tagged on merge."
}

# notes prints CHANGELOG.md's section for version $1, without its heading.
notes() {
	awk -v v="$1" '
		$0 ~ "^## \\[" v "\\]" { on = 1; next }
		on && /^## \[/ { exit }
		on { print }
	' CHANGELOG.md
}

# release_go tags HEAD with the CHANGELOG version and creates its GitHub release, when that
# version is not tagged yet.
release_go() {
	local version
	version="$(go_version)"
	if tag_exists "v$version"; then
		echo "Go: v$version already released."
		return
	fi
	git tag -a "v$version" -m "v$version"
	git push origin "v$version"
	notes "$version" | gh release create "v$version" --title "v$version" --notes-file - --latest
}

# release_py publishes the package to PyPI (unless that version is already there), then tags
# HEAD and creates its GitHub release, when its version is not tagged yet. The publish runs
# before the tag, so a failed upload leaves nothing tagged and the next run retries it.
release_py() {
	local version tag
	version="$(py_version)"
	tag="$PY_TAG_PREFIX$version"
	if tag_exists "$tag"; then
		echo "Python: $version already released."
		return
	fi
	if pypi_versions | grep -qx "$version"; then
		echo "Python: $version is already on PyPI; tagging only."
	else
		(cd "$PY_DIR" && uv build && uv publish)
	fi
	git tag -a "$tag" -m "techai-webutils $version"
	git push origin "$tag"
	gh release create "$tag" --title "techai-webutils $version" --latest=false \
		--notes "techai-webutils $version on PyPI. Changes: CHANGELOG.md."
}

case "${1:-}" in
check)
	check_go
	check_py
	;;
release)
	check_go
	check_py
	release_go
	release_py
	;;
*)
	echo "usage: $0 check|release" >&2
	exit 2
	;;
esac
