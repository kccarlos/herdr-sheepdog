#!/bin/bash
# App Store screenshots on the three required simulators (docs/dev/app-store-review.md
# section 8), from the offline demo, light mode, clean status bar.
#   ios/scripts/appstore-shots.sh <output dir> [large|medium|ipad ...]
# Local only: the files are not committed. Boots one simulator at a time and shuts it down.
set -euo pipefail
cd "$(dirname "$0")/.."
out=${1:?output dir}; shift
sets=("$@"); [ ${#sets[@]} -gt 0 ] || sets=(large medium ipad)
derived=${SHOTS_DERIVED:-$(mktemp -d)}; [ -n "${SHOTS_DERIVED:-}" ] || trap 'rm -rf "$derived"' EXIT
xcodegen generate >/dev/null
for set in "${sets[@]}"; do
  case $set in
    large) device="iPhone Air" ;;
    medium) device="iPhone 17" ;;
    ipad) device="iPad Pro 13-inch (M5)" ;;
    *) echo "unknown set $set" >&2; exit 1 ;;
  esac
  udid=$(xcrun simctl list devices available | grep -F "    $device (" | head -1 | sed -E 's/.*\(([0-9A-F-]{36})\).*/\1/')
  [ -n "$udid" ] || { echo "no simulator named $device" >&2; exit 1; }
  mkdir -p "$out/$set"
  xcrun simctl boot "$udid"
  xcrun simctl bootstatus "$udid" -b >/dev/null
  xcrun simctl ui "$udid" appearance light
  xcrun simctl status_bar "$udid" override --time 9:41 --batteryState discharging --batteryLevel 100 \
    --cellularMode active --cellularBars 4 --wifiMode active --wifiBars 3 --operatorName ""
  TEST_RUNNER_SHEEPDOG_LAUNCH_SHOTS="$out/$set" xcodebuild -scheme Sheepdog -destination "id=$udid" \
    -derivedDataPath "$derived" -skipPackagePluginValidation test \
    -only-testing:SheepdogUITests/LaunchReviewScreenshotsUITests/testAppStoreSet || true
  xcrun simctl shutdown "$udid"
  # App Store Connect rejects an alpha channel: a JPEG round trip drops it.
  for f in "$out/$set"/*.png; do
    sips -s format jpeg "$f" --out "$f.tmp" >/dev/null && sips -s format png "$f.tmp" --out "$f" >/dev/null && rm "$f.tmp"
  done
done
