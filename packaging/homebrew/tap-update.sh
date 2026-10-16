#!/usr/bin/env bash
# Writes a rendered formula into a checkout of kccarlos/homebrew-tap and
# commits it as "sheepdog <version>". It does not push. Used by the homebrew
# job in .github/workflows/release.yml (docs/dev/signing.md).
#
#   packaging/homebrew/tap-update.sh <tap checkout> <formula file> <tag>
#
# - copies the formula to Formula/sheepdog.rb;
# - adds a sheepdog row to the README's app table if the README has a table
#   (a line starting with "| [") and no sheepdog row yet;
# - commits only when something changed, so a rerun is a no-op.
#
# Prints "changed=true" or "changed=false" on its last line.
set -euo pipefail

die() { echo "tap-update: $*" >&2; exit 1; }

tap="${1:?usage: tap-update.sh <tap checkout> <formula file> <tag>}"
formula="${2:?usage: tap-update.sh <tap checkout> <formula file> <tag>}"
tag="${3:?usage: tap-update.sh <tap checkout> <formula file> <tag>}"
[[ "$tag" =~ ^v[0-9] ]] || die "tag must look like v1.2.3, got '$tag'"
[[ -d "$tap/.git" ]] || die "$tap is not a git checkout"
[[ -s "$formula" ]] || die "no formula at $formula"
grep -q '^class Sheepdog < Formula' "$formula" || die "$formula is not the sheepdog formula"

mkdir -p "$tap/Formula"
cp "$formula" "$tap/Formula/sheepdog.rb"

readme="$tap/README.md"
# shellcheck disable=SC2016 # literal backticks (Markdown code)
row='| [sheepdog](https://github.com/kccarlos/herdr-sheepdog) | Manage coding agents running in herdr by voice, away from the screen. | `brew install kccarlos/tap/sheepdog` |'
if [[ -f "$readme" ]] && grep -q '^| \[' "$readme" && ! grep -q 'kccarlos/tap/sheepdog' "$readme"; then
  # Insert after the last row of the first table.
  awk -v row="$row" '
    { lines[NR] = $0 }
    /^\| \[/ && !done { last = NR }
    last && !/^\|/ && !done { done = 1 }
    END { for (i = 1; i <= NR; i++) { print lines[i]; if (i == last) print row } }
  ' "$readme" > "$readme.tmp"
  mv "$readme.tmp" "$readme"
fi

cd "$tap"
git add Formula/sheepdog.rb
[[ -f README.md ]] && git add README.md
if git diff --cached --quiet; then
  echo "tap-update: the tap already has this formula; nothing to commit"
  echo "changed=false"
  exit 0
fi
git -c user.name=KC -c user.email=110118511+kccarlos@users.noreply.github.com \
  commit -q -m "sheepdog ${tag#v}"
git log -1 --format='tap-update: committed %h "%s" as %an <%ae>'
echo "changed=true"
