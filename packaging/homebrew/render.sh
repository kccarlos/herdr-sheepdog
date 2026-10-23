#!/usr/bin/env bash
# Prints the Homebrew formula for a published, signed release to stdout:
#
#   packaging/homebrew/render.sh v0.1.0 > /path/to/homebrew-tap/Formula/sheepdog.rb
#
# Reads the release's checksums.txt with `gh release download` (or
# CHECKSUMS_FILE=<path> for a local copy). Run it only after the darwin
# tarball is signed (release.yml's sign-macos job, or `make release-sign`),
# which replaces it and its checksum. It writes nothing anywhere else and
# pushes nothing: packaging/homebrew/tap-update.sh writes the tap.
set -euo pipefail

tag="${1:?usage: render.sh <tag, e.g. v0.1.0>}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -n "${CHECKSUMS_FILE:-}" ]]; then
  sums="$(cat "$CHECKSUMS_FILE")"
else
  sums="$(gh release download "$tag" --repo kccarlos/herdr-sheepdog --pattern checksums.txt --output -)"
fi

sum_for() {
  local name="sheepdog_${tag}_$1.tar.gz" sum
  sum="$(awk -v n="$name" '$2 == n || $2 == "*"n { print $1 }' <<<"$sums")"
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || { echo "render.sh: no sha256 for $name in checksums.txt" >&2; exit 1; }
  echo "$sum"
}

darwin="$(sum_for darwin_arm64)"
linux="$(sum_for linux_amd64)"

sed -e "s/@TAG@/${tag}/g" \
    -e "s/@SHA256_DARWIN_ARM64@/${darwin}/g" -e "s/@SHA256_LINUX_AMD64@/${linux}/g" \
    "$here/sheepdog.rb.tmpl"
