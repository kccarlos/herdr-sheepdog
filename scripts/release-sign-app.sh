#!/usr/bin/env bash
# Builds, signs, notarizes and staples Sheepdog.app (the Mac app in ios/,
# scheme SheepdogMac) for a published GitHub release, on this Mac, and
# uploads Sheepdog-<version>-macos.zip with its line in checksums.txt.
# release.yml's sign-macos job runs it after scripts/release-sign.sh, with
# the same temporary keychain (SIGN_KEYCHAIN) and API key (NOTARY_KEY*);
# it also runs by hand. See docs/dev/signing.md ("Sheepdog.app").
#
#   scripts/release-sign-app.sh [--dry-run] <tag>
#
# --dry-run   build and sign, but do not notarize, staple or upload.
#
# <version> is the tag without its "v" and without a pre-release suffix
# (v1.2.3-rc1 builds 1.2.3): it becomes MARKETING_VERSION, the app's
# CFBundleShortVersionString. The app is arm64 only, like the daemon.
#
# Idempotent: if checksums.txt already has the zip, nothing is built or
# uploaded (delete the asset and its line to redo a release's app).
#
# Environment (besides scripts/macos-sign.sh's):
#   RELEASE_REPO   owner/repo (default: gh's current repository)

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/macos-sign.sh
source "$here/macos-sign.sh"

die() { echo "release-sign-app: $*" >&2; exit 1; }
log() { echo "release-sign-app: $*"; }

dry_run=0
tag=""
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=1 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    -*) die "unknown flag: $arg" ;;
    *) [[ -z "$tag" ]] || die "only one tag"; tag="$arg" ;;
  esac
done
[[ -n "$tag" ]] || die "usage: $0 [--dry-run] <tag>"
[[ "$tag" =~ ^v[0-9] ]] || die "tag must look like v1.2.3, got '$tag'"
[[ "$(uname -s)" == Darwin ]] || die "run this on a Mac"
command -v xcodegen >/dev/null || die "xcodegen is required"

version="${tag#v}"
version="${version%%-*}"
asset="Sheepdog-${version}-macos.zip"
ios="$here/../ios"
entitlements="$ios/Sources/SheepdogMac/SheepdogMac.entitlements"
[[ -f "$entitlements" ]] || die "no entitlements file at $entitlements"

# gh reads the repository from GH_REPO.
[[ -n "${RELEASE_REPO:-}" ]] && export GH_REPO="$RELEASE_REPO"

work="$(mktemp -d "${TMPDIR:-/tmp}/release-sign-app.XXXXXX")"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/in" "$work/out"

# 1. Already done? checksums.txt lists the zip: nothing to do.
if (( ! dry_run )); then
  command -v gh >/dev/null || die "gh (GitHub CLI) is required"
  gh release download "$tag" -D "$work/in" -p checksums.txt
  [[ -f "$work/in/checksums.txt" ]] || die "release $tag has no checksums.txt"
  if awk -v f="$asset" '$2 == f || $2 == "*" f { found = 1 } END { exit !found }' "$work/in/checksums.txt"; then
    log "$asset is already on release $tag; nothing to do"
    exit 0
  fi
fi

# 2. Archive unsigned: the signature comes from the Developer ID identity in
# the keychain below, not from the project's automatic signing (which is for
# Debug and needs the team's development certificate).
log "building Sheepdog.app $version"
( cd "$ios" && xcodegen generate --spec project.yml )
xcodebuild archive -project "$ios/Sheepdog.xcodeproj" -scheme SheepdogMac \
  -configuration Release -destination 'generic/platform=macOS' \
  -archivePath "$work/Sheepdog.xcarchive" -derivedDataPath "$work/derived" \
  -skipPackagePluginValidation \
  MARKETING_VERSION="$version" ARCHS=arm64 ONLY_ACTIVE_ARCH=NO \
  CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO CODE_SIGN_IDENTITY= \
  | tail -n 20
app="$work/Sheepdog.xcarchive/Products/Applications/Sheepdog.app"
[[ -d "$app" ]] || die "the archive has no Sheepdog.app"
[[ "$(plutil -extract CFBundleShortVersionString raw -o - "$app/Contents/Info.plist")" == "$version" ]] \
  || die "the app's CFBundleShortVersionString is not $version"

# 3. Sign inside-out: nested code first (no entitlements), then the app with
# the entitlements file (the microphone). Hardened runtime and a secure
# timestamp, as for the daemon.
while IFS= read -r nested; do
  msign_sign "$nested"
done < <(find "$app/Contents" \( -name '*.framework' -o -name '*.dylib' -o -name '*.xpc' -o -name '*.appex' \) -prune -print)
SIGN_ENTITLEMENTS="$entitlements" msign_sign "$app"
codesign --verify --deep --strict --verbose=2 "$app"
codesign --display --entitlements - --xml "$app" 2>/dev/null \
  | grep -q 'com.apple.security.device.audio-input' || die "the signed app lacks the audio-input entitlement"

# 4. Notarize and staple: an .app can carry its ticket, so it opens offline.
if (( dry_run )); then
  log "dry run: skipping notarization, stapling and upload"
  exit 0
fi
msign_notarize "$app"
xcrun stapler staple "$app"
xcrun stapler validate "$app"

# 5. Zip the stapled app (ditto keeps the bundle's symlinks and metadata).
ditto -c -k --keepParent "$app" "$work/out/$asset"

# 6. Append the zip to checksums.txt, keep every other line.
sum="$(shasum -a 256 "$work/out/$asset" | awk '{print $1}')"
{ cat "$work/in/checksums.txt"; echo "$sum  $asset"; } > "$work/out/checksums.txt"
log "checksums.txt:"
sed 's/^/  /' "$work/out/checksums.txt"

# 7. Upload.
gh release upload "$tag" --clobber "$work/out/$asset" "$work/out/checksums.txt"
log "uploaded $asset and checksums.txt to release $tag"

# 8. Gatekeeper, as a download would see it (an app, so --type execute works).
log "Gatekeeper check:"
for attempt in 1 2 3 4 5 6; do
  if verdict="$(spctl --assess --type execute -vv "$app" 2>&1)" && grep -q 'source=Notarized Developer ID' <<<"$verdict"; then
    echo "$verdict"
    log "done: $asset on release $tag is signed, notarized and stapled"
    exit 0
  fi
  (( attempt < 6 )) && sleep 10
done
echo "$verdict" >&2
die "Gatekeeper does not report the app as notarized (the upload already happened; rerun after deleting the asset)"
