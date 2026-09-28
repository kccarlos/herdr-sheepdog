#!/usr/bin/env bash
# Signs and notarizes the darwin binary of a published GitHub release, on
# this Mac, and replaces the release's darwin tarball and checksums.txt.
# The release workflow builds on ubuntu only (no macOS runner minutes), so
# the darwin asset is unsigned until this runs. See docs/signing.md.
#
#   scripts/release-sign.sh [--dry-run] <tag>     # or: make release-sign TAG=<tag>
#
# --dry-run   download, verify, sign and check, but do not notarize and do
#             not upload (signing is local; nothing leaves this Mac).
#
# Idempotent: if the release's darwin binary is already signed with a
# Developer ID and Gatekeeper reports it notarized, nothing is re-signed
# or uploaded.
#
# Environment (besides scripts/macos-sign.sh's):
#   RELEASE_REPO             owner/repo (default: gh's current repository)
#   RELEASE_SIGN_LOCAL_DIR   testing: read the tarball and checksums.txt from
#                            this directory instead of `gh release download`,
#                            and never upload
#   RELEASE_SIGN_KEEP        testing, with RELEASE_SIGN_LOCAL_DIR: copy the
#                            re-packed tarball and checksums.txt here

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/macos-sign.sh
source "$here/macos-sign.sh"

die() { echo "release-sign: $*" >&2; exit 1; }
log() { echo "release-sign: $*"; }

dry_run=0
tag=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=1 ;;
    -h|--help) sed -n '2,23p' "$0"; exit 0 ;;
    -*) die "unknown flag: $arg" ;;
    *) [[ -z "$tag" ]] || die "only one tag"; tag="$arg" ;;
  esac
done
[[ -n "$tag" ]] || die "usage: $0 [--dry-run] <tag>"
[[ "$(uname -s)" == Darwin ]] || die "run this on a Mac"

local_dir="${RELEASE_SIGN_LOCAL_DIR:-}"
repo_flag=()
[[ -n "${RELEASE_REPO:-}" ]] && repo_flag=(-R "$RELEASE_REPO")

asset="sheepdog_${tag}_darwin_arm64.tar.gz"
work="$(mktemp -d "${TMPDIR:-/tmp}/release-sign.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/in" "$work/bin" "$work/out"

# 1. Fetch the darwin tarball and checksums.txt.
if [[ -n "$local_dir" ]]; then
  log "using local files from $local_dir (no download, no upload)"
  cp "$local_dir/$asset" "$local_dir/checksums.txt" "$work/in/"
else
  command -v gh >/dev/null || die "gh (GitHub CLI) is required"
  log "downloading $asset and checksums.txt from release $tag"
  gh release download "$tag" ${repo_flag[@]+"${repo_flag[@]}"} -D "$work/in" \
    -p "sheepdog_*_darwin_arm64.tar.gz" -p checksums.txt
fi
[[ -f "$work/in/$asset" ]] || die "release $tag has no $asset"
[[ -f "$work/in/checksums.txt" ]] || die "release $tag has no checksums.txt"

# 2. Verify it against checksums.txt.
want="$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1; exit }' "$work/in/checksums.txt")"
[[ -n "$want" ]] || die "checksums.txt has no line for $asset"
got="$(shasum -a 256 "$work/in/$asset" | awk '{print $1}')"
[[ "$got" == "$want" ]] || die "sha256 mismatch for $asset: checksums.txt says $want, file is $got"
log "sha256 ok ($got)"

# 3. Unpack.
tar -xzf "$work/in/$asset" -C "$work/bin"
bin="$work/bin/sheepdog"
[[ -f "$bin" ]] || die "$asset does not contain sheepdog"
[[ "$(tar -tzf "$work/in/$asset")" == sheepdog ]] || die "$asset holds more than the sheepdog binary"
file "$bin" | grep -q 'Mach-O 64-bit executable arm64' || die "sheepdog in $asset is not a darwin/arm64 binary"

# Already done? Signed with a Developer ID and notarized: nothing to do.
if msign_is_signed "$bin" && msign_assess "$bin" | grep -q 'source=Notarized Developer ID'; then
  log "$asset is already signed and notarized; nothing to do"
  msign_assess "$bin" || true
  exit 0
fi

# 4. Sign (reuses the release binary; only the signature changes).
msign_sign "$bin"

# 5. Notarize.
if (( dry_run )); then
  log "dry run: skipping notarization"
else
  msign_notarize "$bin"
fi

# 6. Re-pack under the same name: a single entry, no macOS metadata.
( cd "$work/bin" && COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata \
    --uid 0 --gid 0 --uname "" --gname "" -czf "$work/out/$asset" sheepdog )
[[ "$(tar -tzf "$work/out/$asset")" == sheepdog ]] || die "re-packed $asset has unexpected entries"

# 7. Replace the darwin line in checksums.txt, keep every other line.
sum="$(shasum -a 256 "$work/out/$asset" | awk '{print $1}')"
awk -v f="$asset" -v s="$sum" '$2 == f || $2 == "*" f { print s "  " f; next } { print }' \
  "$work/in/checksums.txt" > "$work/out/checksums.txt"
log "checksums.txt:"
sed 's/^/  /' "$work/out/checksums.txt"

# 8. Upload, replacing the unsigned assets.
if (( dry_run )) || [[ -n "$local_dir" ]]; then
  log "dry run: not uploading; the signed files were:"
  ls -l "$work/out"
  if [[ -n "$local_dir" && -n "${RELEASE_SIGN_KEEP:-}" ]]; then
    cp "$work/out/$asset" "$work/out/checksums.txt" "$RELEASE_SIGN_KEEP/"
    log "copied them to $RELEASE_SIGN_KEEP"
  fi
else
  gh release upload "$tag" ${repo_flag[@]+"${repo_flag[@]}"} --clobber \
    "$work/out/$asset" "$work/out/checksums.txt"
  log "uploaded $asset and checksums.txt to release $tag"
fi

# 9. Gatekeeper, as a browser download would see it.
# Gatekeeper looks the ticket up online; give a fresh one a moment to show.
log "Gatekeeper check:"
if (( dry_run )); then
  msign_assess "$bin" || log "rejected, as expected without notarization (dry run)"
else
  for attempt in 1 2 3 4 5 6; do
    if verdict="$(msign_assess "$bin")" && grep -q 'source=Notarized Developer ID' <<<"$verdict"; then
      echo "$verdict"
      grep 'source=' <<<"$verdict"
      log "done: $asset on release $tag is signed and notarized"
      exit 0
    fi
    (( attempt < 6 )) && sleep 10
  done
  echo "$verdict" >&2
  die "Gatekeeper does not report the uploaded binary as notarized (the upload already happened; rerun to re-check)"
fi
