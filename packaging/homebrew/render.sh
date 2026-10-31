#!/usr/bin/env bash
# Prints the Homebrew formula (or, with --cask, the Sheepdog.app cask) for a
# published, signed release to stdout:
#
#   packaging/homebrew/render.sh v0.1.0 > /path/to/homebrew-tap/Formula/sheepdog.rb
#   packaging/homebrew/render.sh --cask v0.1.0 > /path/to/homebrew-tap/Casks/sheepdog-app.rb
#
# Reads the release's checksums.txt with `gh release download` (or
# CHECKSUMS_FILE=<path> for a local copy). Run it only after the darwin
# tarball and the app are signed (release.yml's sign-macos job, or `make
# release-sign` and scripts/release-sign-app.sh), which replace or add them
# and their checksums. It writes nothing anywhere else and pushes nothing:
# packaging/homebrew/tap-update.sh writes the tap.
set -euo pipefail

cask=0
if [[ "${1:-}" == --cask ]]; then cask=1; shift; fi
tag="${1:?usage: render.sh [--cask] <tag, e.g. v0.1.0>}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -n "${CHECKSUMS_FILE:-}" ]]; then
  sums="$(cat "$CHECKSUMS_FILE")"
else
  sums="$(gh release download "$tag" --repo kccarlos/herdr-sheepdog --pattern checksums.txt --output -)"
fi

sum_for() {
  local name="$1" sum
  sum="$(awk -v n="$name" '$2 == n || $2 == "*"n { print $1 }' <<<"$sums")"
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || { echo "render.sh: no sha256 for $name in checksums.txt" >&2; exit 1; }
  echo "$sum"
}

if (( cask )); then
  # The zip is named for the version without the "v" and any pre-release
  # suffix (scripts/release-sign-app.sh).
  version="${tag#v}"
  version="${version%%-*}"
  app="$(sum_for "Sheepdog-${version}-macos.zip")"
  sed -e "s/@TAG@/${tag}/g" -e "s/@VERSION@/${version}/g" -e "s/@SHA256_APP@/${app}/g" \
      "$here/sheepdog-app.rb.tmpl"
  exit 0
fi

darwin="$(sum_for "sheepdog_${tag}_darwin_arm64.tar.gz")"
linux="$(sum_for "sheepdog_${tag}_linux_amd64.tar.gz")"

sed -e "s/@TAG@/${tag}/g" \
    -e "s/@SHA256_DARWIN_ARM64@/${darwin}/g" -e "s/@SHA256_LINUX_AMD64@/${linux}/g" \
    "$here/sheepdog.rb.tmpl"
