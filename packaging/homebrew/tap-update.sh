#!/usr/bin/env bash
# Writes a rendered formula and cask into a checkout of
# kccarlos/homebrew-tap and commits them together as "sheepdog <version>".
# It does not push. Used by the homebrew job in
# .github/workflows/release.yml (docs/dev/signing.md).
#
#   packaging/homebrew/tap-update.sh <tap checkout> <formula file> <cask file> <tag>
#
# - copies the formula to Formula/sheepdog.rb and the cask to
#   Casks/sheepdog-app.rb, after checking both are for <tag>;
# - fails, writing nothing, if only one of the two differs from the tap: a
#   release publishes the daemon and the app together;
# - adds a sheepdog row and a sheepdog-app row to the README's app table if
#   the README has a table (a line starting with "| [") and lacks them;
# - commits only when something changed, so a rerun is a no-op.
#
# Prints "changed=true" or "changed=false" on its last line.
set -euo pipefail

die() { echo "tap-update: $*" >&2; exit 1; }

usage="usage: tap-update.sh <tap checkout> <formula file> <cask file> <tag>"
tap="${1:?$usage}"
formula="${2:?$usage}"
cask="${3:?$usage}"
tag="${4:?$usage}"
[[ "$tag" =~ ^v[0-9] ]] || die "tag must look like v1.2.3, got '$tag'"
[[ -d "$tap/.git" ]] || die "$tap is not a git checkout"
[[ -s "$formula" ]] || die "no formula at $formula"
[[ -s "$cask" ]] || die "no cask at $cask"
grep -q '^class Sheepdog < Formula' "$formula" || die "$formula is not the sheepdog formula"
grep -q '^cask "sheepdog-app" do' "$cask" || die "$cask is not the sheepdog-app cask"
grep -q "/download/$tag/" "$formula" || die "$formula is not for $tag"
grep -q "/download/$tag/" "$cask" || die "$cask is not for $tag"

# A missing file counts as changed.
changed_formula=1; changed_cask=1
cmp -s "$formula" "$tap/Formula/sheepdog.rb" 2>/dev/null && changed_formula=0
cmp -s "$cask" "$tap/Casks/sheepdog-app.rb" 2>/dev/null && changed_cask=0
if (( changed_formula != changed_cask )); then
  (( changed_formula )) && only=formula || only=cask
  die "only the $only would change in the tap; the formula and the cask must be updated together"
fi

mkdir -p "$tap/Formula" "$tap/Casks"
cp "$formula" "$tap/Formula/sheepdog.rb"
cp "$cask" "$tap/Casks/sheepdog-app.rb"

readme="$tap/README.md"
# add_row <marker> <row>: after the last row of the README's first table,
# unless a line already has <marker>.
add_row() {
  if [[ -f "$readme" ]] && grep -q '^| \[' "$readme" && ! grep -qF "$1" "$readme"; then
    awk -v row="$2" '
      { lines[NR] = $0 }
      /^\| \[/ && !done { last = NR }
      last && !/^\|/ && !done { done = 1 }
      END { for (i = 1; i <= NR; i++) { print lines[i]; if (i == last) print row } }
    ' "$readme" > "$readme.tmp"
    mv "$readme.tmp" "$readme"
  fi
}
# shellcheck disable=SC2016 # literal backticks (Markdown code)
add_row 'kccarlos/tap/sheepdog`' '| [sheepdog](https://github.com/kccarlos/herdr-sheepdog) | Manage coding agents running in herdr by voice, away from the screen. | `brew install kccarlos/tap/sheepdog` |'
# shellcheck disable=SC2016
add_row 'kccarlos/tap/sheepdog-app' '| [Sheepdog.app](https://github.com/kccarlos/herdr-sheepdog) | The Mac menu-bar app for talking to sheepdog. | `brew install --cask kccarlos/tap/sheepdog-app` |'

cd "$tap"
git add Formula/sheepdog.rb Casks/sheepdog-app.rb
[[ -f README.md ]] && git add README.md
if git diff --cached --quiet; then
  echo "tap-update: the tap already has this formula and cask; nothing to commit"
  echo "changed=false"
  exit 0
fi
git -c user.name=KC -c user.email=110118511+kccarlos@users.noreply.github.com \
  commit -q -m "sheepdog ${tag#v}"
git log -1 --format='tap-update: committed %h "%s" as %an <%ae>'
echo "changed=true"
