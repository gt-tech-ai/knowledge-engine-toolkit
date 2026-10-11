#!/usr/bin/env bash
#
# sync.sh — vendor the Opengrep community rules into .semgrep/rules/community/.
#
# Usage: .semgrep/sync.sh [<commit-sha>]
#
# Clones github.com/opengrep/opengrep-rules at <commit-sha> (default: the pinned
# SHA below), copies the security/ and correctness/ rule subtrees of go and
# python into .semgrep/rules/community/, keeps only the rule YAML (the per-rule
# test fixtures and any *.test.yaml are left behind), deletes the rules in the
# DROPPED table below, and copies LICENSE. SOURCE is hand-maintained; update its
# Commit/Date lines when the SHA changes. Re-running with the same SHA produces a
# clean `git diff`.
#
# Offline / air-gapped: set OPENGREP_RULES_SRC=<path-to-a-local-opengrep-rules
# checkout> to copy from that checkout instead of cloning over the network.
set -euo pipefail

# The vendored snapshot. Bump deliberately: pass a new SHA and review the diff.
PINNED_SHA="f1d2b562b414783763fd02a6ed2736eaed622efa"
SHA="${1:-$PINNED_SHA}"
REPO_URL="https://github.com/opengrep/opengrep-rules"
LANGS="go python"

# Rules removed after copying: each is owned by another gate or is a false positive here.
DROPPED=(
  incorrect-default-permission # gosec G301/G306 (golangci-lint) owns it; this rule also flags MkdirAll(dir, 0o700), which gosec accepts.
  dangerous-exec-command       # gosec G204 (golangci-lint) owns it; both scanners on one exec site would mean two suppressions.
  string-formatted-query       # gosec G201/G202 (golangci-lint) owns it.
  math-random-used             # gosec G404 (golangci-lint) owns it.
  no-direct-write-to-responsewriter # false positive for a JSON-only API (all writes are JSON-encoded).
)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEST="$SCRIPT_DIR/rules/community"

# --- obtain the source checkout -------------------------------------------------
CLEANUP=""
if [[ -n "${OPENGREP_RULES_SRC:-}" ]]; then
  SRC="$OPENGREP_RULES_SRC"
  echo "sync: copying from local source $SRC"
else
  SRC="$(mktemp -d)"
  CLEANUP="$SRC"
  echo "sync: cloning $REPO_URL @ $SHA"
  git clone --quiet "$REPO_URL" "$SRC"
  git -C "$SRC" checkout --quiet "$SHA"
fi
trap '[[ -n "$CLEANUP" ]] && rm -rf "$CLEANUP"' EXIT

RESOLVED_SHA="$(git -C "$SRC" rev-parse HEAD)"

# --- copy the security/ and correctness/ rule YAML, preserving paths ------------
# SOURCE is hand-maintained, so keep it across the wipe.
SOURCE_BAK="$(mktemp)"
cp "$DEST/SOURCE" "$SOURCE_BAK"
rm -rf "$DEST"
mkdir -p "$DEST"
mv "$SOURCE_BAK" "$DEST/SOURCE"
(
  cd "$SRC"
  for lang in $LANGS; do
    [[ -d "$lang" ]] || continue
    find "$lang" -type d \( -name security -o -name correctness \) -print0 |
      while IFS= read -r -d '' dir; do
        find "$dir" -type f \( -name '*.yaml' -o -name '*.yml' \) \
          ! -name '*.test.yaml' ! -name '*.test.yml' -print0 |
          while IFS= read -r -d '' f; do
            rel="${f#./}"
            mkdir -p "$DEST/$(dirname "$rel")"
            cp "$f" "$DEST/$rel"
          done
      done
  done
)

# --- delete the dropped rules ---------------------------------------------------
for rule in "${DROPPED[@]}"; do
  matches=()
  while IFS= read -r -d '' f; do
    if grep -Eq "^[[:space:]]*-[[:space:]]+id:[[:space:]]+[\"']?${rule}[\"']?[[:space:]]*\$" "$f"; then
      matches+=("$f")
    fi
  done < <(find "$DEST" -type f \( -name '*.yaml' -o -name '*.yml' \) -print0)

  if [[ ${#matches[@]} -eq 0 ]]; then
    echo "sync: ERROR dropped rule '$rule' matches no vendored rule" >&2
    exit 1
  fi
  for f in "${matches[@]}"; do
    n="$(grep -Ec '^[[:space:]]*-[[:space:]]+id:' "$f")"
    if [[ "$n" -ne 1 ]]; then
      echo "sync: ERROR dropped rule '$rule' lives in multi-rule file ${f#"$DEST"/} ($n rules); drop-by-id needs a different mechanism" >&2
      exit 1
    fi
    rm -f "$f"
    echo "sync: dropped $rule (${f#"$DEST"/})"
  done
done

# --- LICENSE --------------------------------------------------------------------
cp "$SRC/LICENSE" "$DEST/LICENSE"

count="$(find "$DEST" -type f \( -name '*.yaml' -o -name '*.yml' \) | wc -l | tr -d ' ')"
echo "sync: vendored $count rule files from $RESOLVED_SHA into .semgrep/rules/community"
