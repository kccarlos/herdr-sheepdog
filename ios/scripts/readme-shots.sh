#!/bin/bash
# The README's and docs/iphone-app.md's screenshots, from the offline demo:
# conversation, an approval, Agents grouped by who needs you, the agent
# window, Files, a Markdown file, Changes, dark mode and iPad. It reuses the
# screenshot hooks of the existing UI tests (docs/dev/development.md#screenshots):
#   LaunchReviewScreenshotsUITests/testAppStoreSet, RedesignShotsUITests/testAgentsPage,
#   FilesDemoUITests, ChangesDemoUITests.
#
#   ios/scripts/readme-shots.sh [output dir]      # default: assets/readme/app
#
# Local only, never in CI. One simulator at a time, booted and shut down again:
# the iPhone 18 Pro below for the phone shots and the iPad Air 11-inch (M4) for
# the iPad shot. Status bar 9:41, full battery, light mode (dark for the dark
# shot). Each PNG is scaled to 540 px wide (iPad: 1100) to stay well under 400 KB.
set -euo pipefail
cd "$(dirname "$0")/.."
out=${1:-../assets/readme/app}
phone=${SHOTS_PHONE:-E0CEECF1-202B-497F-AD1D-B0B90193D1C0}
ipad=${SHOTS_IPAD:-1B6602A4-C05F-4933-92F2-9D300FA6148E}
derived=${SHOTS_DERIVED:-$(mktemp -d)}; raw=$(mktemp -d)
trap 'rm -rf "$raw"; [ -n "${SHOTS_DERIVED:-}" ] || rm -rf "$derived"' EXIT
mkdir -p "$out"
out=$(cd "$out" && pwd)
xcodegen generate >/dev/null

# run_tests <udid> <appearance> <dir> <only-testing...>
run_tests() {
  local udid=$1 look=$2 dir=$3; shift 3
  mkdir -p "$dir"
  xcrun simctl boot "$udid" 2>/dev/null || true
  xcrun simctl bootstatus "$udid" -b >/dev/null
  xcrun simctl ui "$udid" appearance "$look"
  xcrun simctl status_bar "$udid" override --time 9:41 --batteryState discharging --batteryLevel 100 \
    --cellularMode active --cellularBars 4 --wifiMode active --wifiBars 3 --operatorName ""
  local only=()
  for t in "$@"; do only+=("-only-testing:SheepdogUITests/$t"); done
  TEST_RUNNER_SHEEPDOG_LAUNCH_SHOTS="$dir" TEST_RUNNER_SHEEPDOG_REDESIGN_SHOTS="$dir" \
    TEST_RUNNER_SHEEPDOG_SHOTS_SUFFIX=light TEST_RUNNER_SHEEPDOG_DEMO_SHOTS="$dir" \
    xcodebuild -scheme Sheepdog -destination "id=$udid" -derivedDataPath "$derived" \
    -skipPackagePluginValidation test "${only[@]}" || true
  xcrun simctl shutdown "$udid" || true
}

# pick <dir> <file> <name> <width>: the shot, scaled, as <out>/<name>.png
pick() {
  [ -f "$1/$2" ] || { echo "missing $1/$2" >&2; exit 1; }
  sips --resampleWidth "$4" "$1/$2" --out "$out/$3.png" >/dev/null
}

mkdir -p "$raw/light" "$raw/dark" "$raw/ipad"
run_tests "$phone" light "$raw/light" \
  LaunchReviewScreenshotsUITests/testAppStoreSet RedesignShotsUITests/testAgentsPage \
  FilesDemoUITests/testBrowseAFolderAndReadAMarkdownFile ChangesDemoUITests/testChangesListAndDiff
run_tests "$phone" dark "$raw/dark" LaunchReviewScreenshotsUITests/testAppStoreSet
run_tests "$ipad" light "$raw/ipad" LaunchReviewScreenshotsUITests/testAppStoreSet

pick "$raw/light" 05-conversation.png conversation 540
pick "$raw/light" 01-approval.png approval 540
pick "$raw/light" ui-agents-waiting-light.png agents 540
pick "$raw/light" ui-agent-window-light.png agent-window 540
pick "$raw/light" files-list.png files 540
pick "$raw/light" viewer-markdown.png markdown 540
pick "$raw/light" changes-diff.png changes 540
pick "$raw/dark" 01-approval.png dark-approval 540
# The iPad is shot in landscape, but the simulator's framebuffer stays portrait: turn it back.
f="$raw/ipad/02-agents.png"
if [ "$(sips -g pixelWidth "$f" | awk '/pixelWidth/{print $2}')" -lt "$(sips -g pixelHeight "$f" | awk '/pixelHeight/{print $2}')" ]; then
  sips -r 270 "$f" --out "$f" >/dev/null
fi
swift scripts/hide-resize-handle.swift "$f"
pick "$raw/ipad" 02-agents.png ipad 1100
ls -l "$out"
