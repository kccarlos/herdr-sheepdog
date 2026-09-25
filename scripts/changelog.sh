#!/bin/sh
# Generates a Conventional Commits changelog, grouped into Breaking
# changes/Features/Fixes/Performance/Documentation/Build & CI/Other,
# for one git revision range. `make changelog` writes it to CHANGELOG.md;
# release.yml (once tags exist) can instead capture it as the GitHub
# Release body for just the tag being released.
#
# usage: scripts/changelog.sh [RANGE] [HEADING]
#   RANGE    a git revision range, e.g. "v1.0.0..v1.1.0"; omitted (or
#            empty) means the full history — there are no tags yet, so
#            that is what `make changelog` uses: the initial commit is
#            the base.
#   HEADING  the top section's title (default "Unreleased").
set -eu
RANGE=${1:-}
HEADING=${2:-Unreleased}

ALL=$(mktemp)
TMP=$(mktemp)
trap 'rm -f "$ALL" "$TMP"' EXIT
# shellcheck disable=SC2086 # RANGE is a single git revision-range token or empty, never quoted on purpose
git log --no-merges --format='%h %s' $RANGE >"$ALL"

# A breaking commit (`type!:` or `type(scope)!:`) is listed once, under
# "Breaking changes" at the top, whatever its type; the other sections
# are filled from the rest.
BREAKING='^[0-9a-f]+ [A-Za-z]+(\([^)]*\))?!:'
breaking=$(grep -E "$BREAKING" "$ALL" || true)
grep -Ev "$BREAKING" "$ALL" >"$TMP" || true

bucket() { grep -E "$1" "$TMP" || true; }
feat=$(bucket '^[0-9a-f]+ feat(\(|!|:)')
fix=$(bucket '^[0-9a-f]+ fix(\(|!|:)')
perf=$(bucket '^[0-9a-f]+ perf(\(|!|:)')
docs=$(bucket '^[0-9a-f]+ docs(\(|!|:)')
buildci=$(bucket '^[0-9a-f]+ (build|ci)(\(|!|:)')
other=$(grep -Ev '^[0-9a-f]+ (feat|fix|perf|docs|build|ci)(\(|!|:)' "$TMP" || true)

section() { # title, entries (hash-prefixed lines)
  [ -z "$2" ] && return 0
  printf '### %s\n\n' "$1"
  echo "$2" | while read -r hash subject; do printf -- '- %s (%s)\n' "$subject" "$hash"; done
  printf '\n'
}

printf '## %s\n\n' "$HEADING"
section "Breaking changes" "$breaking"
section "Features" "$feat"
section "Fixes" "$fix"
section "Performance" "$perf"
section "Documentation" "$docs"
section "Build & CI" "$buildci"
section "Other" "$other"
