#!/usr/bin/env bash
# The launch video's phone take: the iOS app's offline demo in the iOS
# Simulator, driven by LaunchRecordingUITests while `simctl io recordVideo`
# films it.
#
#   SIM=<udid of an existing iPhone simulator> WORK=/tmp/launch scripts/readme-media/launch/phone.sh
#
# SIM must be an existing simulator (this script never creates one). It is
# booted, set to dark mode with a 9:41 status bar, and left booted. Writes
# $WORK/phone/{rec.mp4,cfr.mp4,marks.txt,rec.t0}. SKIP_BUILD=1 reuses
# $WORK/phone/dd.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
ios=$repo/ios
work=${WORK:?set WORK}/phone
u=${SIM:?set SIM to an existing simulator udid}
mkdir -p "$work"

if [ -z "${SKIP_BUILD:-}" ]; then
	(cd "$ios" && xcodegen generate >/dev/null)
	xcodebuild build-for-testing -project "$ios/Sheepdog.xcodeproj" -scheme Sheepdog -destination "id=$u" \
		-derivedDataPath "$work/dd" -skipPackagePluginValidation >"$work/build.log" 2>&1
fi
xcrun simctl boot "$u" 2>/dev/null || true
xcrun simctl bootstatus "$u" -b >/dev/null
xcrun simctl ui "$u" appearance dark
xcrun simctl status_bar "$u" override --time 9:41 --batteryState discharging --batteryLevel 100 \
	--cellularBars 4 --wifiBars 3 --dataNetwork wifi --operatorName ""
xctestrun=$(ls "$work"/dd/Build/Products/*.xctestrun | head -1)

marks=$work/launch-marks.txt
log=$work/launch-test.log
rm -rf "$marks" "$work/rec.mp4" "$work"/r.xcresult
: >"$marks"
: >"$log"
now() { python3 -c 'import time; print(time.time())'; }
xcrun simctl io "$u" recordVideo --codec=h264 --force "$work/rec.mp4" 2>"$work/rec.log" &
rec=$!
trap 'kill -INT $rec 2>/dev/null || true' EXIT
until grep -q "Recording started" "$work/rec.log" 2>/dev/null; do
	kill -0 $rec 2>/dev/null || { cat "$work/rec.log" >&2; exit 1; }
	sleep 0.05
done
TEST_RUNNER_SHEEPDOG_LAUNCH_MARKS=$marks \
	xcodebuild test-without-building -xctestrun "$xctestrun" -destination "id=$u" -resultBundlePath "$work/r.xcresult" \
	-only-testing:SheepdogUITests/LaunchRecordingUITests/testRecordLaunchVideo >"$log" 2>&1 &
xcb=$!
# xcodebuild can sit for minutes after a crashed runner: stop on the
# first sign of failure.
until grep -q '^end ' "$marks" || grep -q -e "^Test Case .* failed" -e "Restarting after unexpected exit" "$log" ||
	! kill -0 $xcb 2>/dev/null; do sleep 0.1; done
t1=$(now)
kill -INT $rec
wait $rec || true
trap - EXIT
for _ in $(seq 120); do kill -0 $xcb 2>/dev/null || break; sleep 0.5; done
kill $xcb 2>/dev/null || true
grep -q '^end ' "$marks" || { echo "the UI test failed; see $log" >&2; exit 1; }
dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$work/rec.mp4")
python3 -c "print($t1 - $dur)" >"$work/rec.t0"
mv "$marks" "$work/marks.txt"
ffmpeg -loglevel error -y -i "$work/rec.mp4" -vf fps=30 -c:v libx264 -crf 14 -preset veryfast "$work/cfr.mp4"
echo "phone take in $work (marks.txt, rec.t0, cfr.mp4)"
