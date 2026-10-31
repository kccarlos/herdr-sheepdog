#!/usr/bin/env bash
# Writes a rendered formula, and optionally a cask, into a checkout of
# kccarlos/homebrew-tap and commits them as "sheepdog <version>". It does not
# push. Used by the homebrew job in .github/workflows/release.yml
# (docs/dev/signing.md).
#
#   packaging/homebrew/tap-update.sh <tap checkout> <formula file> [<cask file>] <tag>
#
# - copies the formula to Formula/sheepdog.rb and, when given, the cask to
#   Casks/sheepdog-app.rb, after checking every given file is for <tag>;
# - adds a sheepdog row, and for a cask a sheepdog-app row, to the README's
#   app table if the README has a table (a line starting with "| [") and
#   lacks it;
# - commits only when something changed, so a rerun is a no-op.
#
# Prints "changed=true" or "changed=false" on its last line.
set -euo pipefail

die() { echo "tap-update: $*" >&2; exit 1; }

usage="usage: tap-update.sh <tap checkout> <formula file> [<cask file>] <tag>"
case $# in
  3) tap="$1"; formula="$2"; cask=""; tag="$3" ;;
  4) tap="$1"; formula="$2"; cask="$3"; tag="$4" ;;
  *) die "$usage" ;;
esac
[[ "$tag" =~ ^v[0-9] ]] || die "tag must look like v1.2.3, got '$tag'"
[[ -d "$tap/.git" ]] || die "$tap is not a git checkout"
[[ -s "$formula" ]] || die "no formula at $formula"
grep -q '^class Sheepdog < Formula' "$formula" || die "$formula is not the sheepdog formula"
grep -q "/download/$tag/" "$formula" || die "$formula is not for $tag"
if [[ -n "$cask" ]]; then
  [[ -s "$cask" ]] || die "no cask at $cask"
  grep -q '^cask "sheepdog-app" do' "$cask" || die "$cask is not the sheepdog-app cask"
  grep -q "/download/$tag/" "$cask" || die "$cask is not for $tag"
fi

mkdir -p "$tap/Formula"
cp "$formula" "$tap/Formula/sheepdog.rb"
if [[ -n "$cask" ]]; then
  mkdir -p "$tap/Casks"
  cp "$cask" "$tap/Casks/sheepdog-app.rb"
fi

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
if [[ -n "$cask" ]]; then
  # shellcheck disable=SC2016
  add_row 'kccarlos/tap/sheepdog-app' '| [Sheepdog.app](https://github.com/kccarlos/herdr-sheepdog) | The Mac menu-bar app for talking to sheepdog. | `brew install --cask kccarlos/tap/sheepdog-app` |'
fi

cd "$tap"
git add Formula/sheepdog.rb
[[ -n "$cask" ]] && git add Casks/sheepdog-app.rb
[[ -f README.md ]] && git add README.md
if git diff --cached --quiet; then
  echo "tap-update: the tap already has this formula${cask:+ and cask}; nothing to commit"
  echo "changed=false"
  exit 0
fi
git -c user.name=KC -c user.email=110118511+kccarlos@users.noreply.github.com \
  commit -q -m "sheepdog ${tag#v}"
git log -1 --format='tap-update: committed %h "%s" as %an <%ae>'
echo "changed=true"
