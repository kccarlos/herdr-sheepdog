#!/usr/bin/env bash
# The launch video's phone take: the iOS app's offline demo in the iOS
# Simulator, driven by the iOS repo's LaunchRecordingUITests while
# `simctl io recordVideo` films it, plus the lock screen with the approval
# notification as two screenshots.
#
#   IOS_REPO=/path/to/herdr-sheepdog-ios WORK=/tmp/launch scripts/readme-media/launch/phone.sh
#
# Two passes on the simulator "$SIM_NAME" (made when missing; default
# "Sheepdog Launch"), light mode, 9:41 status bar:
#   1. lock: hero/phone.sh's HeroRecordingUITests with notifications
#      allowed, `xcrun simctl push` of the daemon's own payload
#      (hero/payload.go): lock0.png and lock1.png. Its video is thrown
#      away: on the iOS 27 simulator the display stays frozen after an
#      unlock, so nothing recorded after it moves.
#   2. app: the simulator is rebooted and LaunchRecordingUITests (no lock)
#      is filmed: rec.mp4, cfr.mp4, rec.t0 and marks.txt.
# Needs IOS_REPO at a commit with both tests. SKIP_BUILD=1 reuses $WORK/phone/dd.
# The fixture daemon of hero/phone.sh (empty config, no herdr, no model) is
# the warm-up's connection, on $FIXTURE_PORT (default 7796).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
ios=$(cd "${IOS_REPO:?set IOS_REPO to a herdr-sheepdog-ios checkout with LaunchRecordingUITests}" && pwd)
work=${WORK:?set WORK}/phone
mkdir -p "$work"
name=${SIM_NAME:-Sheepdog Launch}
bundle=com.github.kccarlos.herdr-sheepdog-ios

udid() { xcrun simctl list devices available | awk -v n="$name" -F '[()]' 'index($0, n" (") {print $2; exit}'; }
u=$(udid)
if [ -z "$u" ]; then u=$(xcrun simctl create "$name" "${DEVICE_TYPE:-iPhone 18 Pro}"); fi
echo "$u" >"$work/udid"

if [ -z "${SKIP_PASS1:-}" ]; then
	# pass 1: the lock screen (hero/phone.sh keeps its files in $WORK/phone)
	SIM_NAME=$name SKIP_BUILD=${SKIP_BUILD:-} IOS_REPO=$ios WORK=${WORK:?} UNLOCK_SETTLE=0.5 \
		"$repo/scripts/readme-media/hero/phone.sh" || echo "pass 1: the video after the unlock froze, as expected; the lock screenshots are kept" >&2
	[ -f "$work/lock1.png" ] || { echo "no lock screenshot" >&2; exit 1; }
fi

# pass 2: the app, on a freshly booted simulator
xcrun simctl shutdown "$u" 2>/dev/null || true
xcrun simctl boot "$u"
xcrun simctl bootstatus "$u" -b >/dev/null
xcrun simctl ui "$u" appearance light
xcrun simctl status_bar "$u" override --time 9:41 --batteryState charged --batteryLevel 100 \
	--cellularBars 4 --wifiBars 3 --dataNetwork wifi --operatorName ""
xcrun simctl privacy "$u" grant microphone "$bundle" 2>/dev/null || true

if [ -z "${SKIP_BUILD:-}" ]; then
	(cd "$ios" && xcodegen generate >/dev/null)
	xcodebuild build-for-testing -project "$ios/Sheepdog.xcodeproj" -scheme Sheepdog \
		-destination "id=$u" -derivedDataPath "$work/dd" CODE_SIGNING_ALLOWED=NO >"$work/build.log" 2>&1
fi
xctestrun=$(ls "$work"/dd/Build/Products/*.xctestrun | head -1)

marks=$work/launch-marks.txt
rm -f "$marks" "$work/rec.mp4" "$work/rec.t0"
: >"$marks"
now() { python3 -c 'import time; print(time.time())'; }
xcrun simctl io "$u" recordVideo --codec=h264 --force "$work/rec.mp4" 2>"$work/rec.log" &
rec=$!
trap 'kill -INT $rec 2>/dev/null || true' EXIT
until grep -q "Recording started" "$work/rec.log" 2>/dev/null; do
	kill -0 $rec 2>/dev/null || { cat "$work/rec.log" >&2; exit 1; }
	sleep 0.05
done
TEST_RUNNER_SHEEPDOG_LAUNCH_MARKS=$marks \
	xcodebuild test-without-building -xctestrun "$xctestrun" -destination "id=$u" \
	-only-testing:SheepdogUITests/LaunchRecordingUITests >"$work/launch-test.log" 2>&1 &
xcb=$!
until grep -q '^end ' "$marks" || ! kill -0 $xcb 2>/dev/null; do sleep 0.1; done
t1=$(now)
kill -INT $rec; wait $rec || true
trap - EXIT
kill $xcb 2>/dev/null || true
wait $xcb 2>/dev/null || true
grep -q '^end ' "$marks" || { echo "the UI test failed; see $work/launch-test.log" >&2; exit 1; }
dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$work/rec.mp4")
frames=$(ffprobe -v error -count_frames -select_streams v:0 -show_entries stream=nb_read_frames -of csv=p=0 "$work/rec.mp4")
[ "$frames" -gt 100 ] || { echo "the recording froze ($frames frames)" >&2; exit 1; }
python3 -c "print($t1 - $dur)" >"$work/rec.t0"
mv "$work/launch-marks.txt" "$work/marks.txt"
ffmpeg -loglevel error -y -i "$work/rec.mp4" -vf fps=30 -c:v libx264 -crf 14 -preset veryfast "$work/cfr.mp4"
echo "phone take in $work (marks.txt, lock{0,1}.png, rec.t0, cfr.mp4)"
