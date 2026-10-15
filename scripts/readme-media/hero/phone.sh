#!/usr/bin/env bash
# The hero video's phone take: the iOS app's offline demo in the iOS
# Simulator, driven by the iOS repo's HeroRecordingUITests while
# `simctl io recordVideo` films it. When the test marks `push_now`, this
# script sends billing's approval as a notification with `xcrun simctl
# push` (the payload is the daemon's own, from payload.go).
#
#   IOS_REPO=/path/to/herdr-sheepdog-ios WORK=/tmp/hero scripts/readme-media/hero/phone.sh
#
# Writes $WORK/phone/lock0.png and lock1.png (the lock screen before and
# after the notification), cfr2.mp4 (the app after the unlock), rec2.t0
# (the video's start in unix seconds) and marks.txt for compose.py. A throwaway `--voice=off --brain=fake
# --herdr=mock` daemon on $FIXTURE_PORT (default 7796), with an empty
# config and its own data dir, is the warm-up's connection (it is how the
# app gets notification permission); it never touches herdr or a model.
# Uses its own "Sheepdog README" simulator in light mode with a 9:41 status
# bar. SKIP_BUILD=1 reuses the last build in $WORK/phone/dd.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
ios=$(cd "${IOS_REPO:?set IOS_REPO to a herdr-sheepdog-ios checkout with HeroRecordingUITests}" && pwd)
work=${WORK:?set WORK}/phone
mkdir -p "$work"
device_type=${DEVICE_TYPE:-iPhone 18 Pro}
name="Sheepdog README"
bundle=com.github.kccarlos.herdr-sheepdog-ios
port=${FIXTURE_PORT:-7796}

udid=$(xcrun simctl list devices available | awk -v n="$name" -F '[()]' 'index($0, n" (") {print $2; exit}')
if [ -z "$udid" ]; then
	udid=$(xcrun simctl create "$name" "$device_type")
fi
xcrun simctl boot "$udid" 2>/dev/null || true
xcrun simctl bootstatus "$udid" -b >/dev/null
xcrun simctl ui "$udid" appearance light
xcrun simctl status_bar "$udid" override --time 9:41 --batteryState charged --batteryLevel 100 \
	--cellularBars 4 --wifiBars 3 --dataNetwork wifi --operatorName ""
# A fresh install: no notification left on the lock screen from a last
# take (the warm-up allows notifications again).
xcrun simctl uninstall "$udid" "$bundle" 2>/dev/null || true
xcrun simctl privacy "$udid" grant microphone "$bundle" 2>/dev/null || true
xcrun simctl spawn "$udid" defaults write com.apple.keyboard.preferences DidShowContinuousPathIntroduction -bool true
xcrun simctl spawn "$udid" defaults write com.apple.keyboard.preferences DidShowGestureKeyboardIntroduction -bool true

(cd "$repo" && go build -o "$work/sheepdog" ./cmd/sheepdog && go run scripts/readme-media/hero/payload.go >"$work/payload.apns")

if [ -z "${SKIP_BUILD:-}" ]; then
	(cd "$ios" && xcodegen generate >/dev/null)
	xcodebuild build-for-testing -project "$ios/Sheepdog.xcodeproj" -scheme Sheepdog \
		-destination "id=$udid" -derivedDataPath "$work/dd" CODE_SIGNING_ALLOWED=NO >"$work/build.log" 2>&1
fi
xctestrun=$(ls "$work"/dd/Build/Products/*.xctestrun | head -1)

# The warm-up's daemon: empty config, own data dir, no herdr, no model.
rm -rf "$work/fixture" && mkdir -p "$work/fixture" && : >"$work/fixture/config.toml"
SHEEPDOG_DATA_DIR="$work/fixture/data" SHEEPDOG_STATE_PATH="$work/fixture/state.json" \
	"$work/sheepdog" serve --config "$work/fixture/config.toml" --env-file /dev/null \
	--brain=fake --herdr=mock --voice=off --dev --port "$port" >"$work/fixture/out.log" 2>&1 &
fixture=$!
rec=""
cleanup() {
	kill "$fixture" 2>/dev/null || true
	[ -n "$rec" ] && kill -INT "$rec" 2>/dev/null || true
}
trap cleanup EXIT
until curl -sf "http://127.0.0.1:$port/api/state" >/dev/null; do sleep 0.2; done

marks=$work/marks.txt
ready=$work/rec2.ready
rm -f "$marks" "$ready" "$work"/rec*.mp4 "$work"/rec*.t0
: >"$marks"
now() { python3 -c 'import time; print(time.time())'; }
# record N: starts recording $work/recN.mp4 and waits until it runs.
record() {
	xcrun simctl io "$udid" recordVideo --codec=h264 --force "$work/rec$1.mp4" 2>"$work/rec$1.log" &
	rec=$!
	until grep -q "Recording started" "$work/rec$1.log" 2>/dev/null; do
		kill -0 $rec 2>/dev/null || { cat "$work/rec$1.log" >&2; exit 1; }
		sleep 0.05
	done
}
# stop N: stops it. simctl writes a frame only when the screen changes,
# so the video's first frame can lag its start: its clock is counted back
# from the stop instead (recN.t0, unix seconds at the video's 0).
stop() {
	local t1
	t1=$(now)
	kill -INT $rec
	wait $rec || true
	rec=""
	local dur
	dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$work/rec$1.mp4")
	python3 -c "print($t1 - $dur)" >"$work/rec$1.t0"
}

# The lock screen is shot as two screenshots (before and after the
# notification): the simulator's recording freezes across a lock or an
# unlock. The video starts once the app is open again (the test marks
# `unlocked` and waits for $ready) and stops at `end`.
TEST_RUNNER_SHEEPDOG_HERO_MARKS=$marks TEST_RUNNER_SHEEPDOG_HERO_READY=$ready TEST_RUNNER_SHEEPDOG_FIXTURE_PORT=$port \
	xcodebuild test-without-building -xctestrun "$xctestrun" -destination "id=$udid" \
	-only-testing:SheepdogUITests/HeroRecordingUITests >"$work/test.log" 2>&1 &
xcb=$!
phase=0
while kill -0 $xcb 2>/dev/null && ! grep -qE "Test Suite 'Selected tests' (passed|failed)" "$work/test.log"; do
	if [ $phase = 0 ] && grep -q '^locked ' "$marks"; then
		sleep 0.8
		xcrun simctl io "$udid" screenshot "$work/lock0.png" >/dev/null 2>&1
		phase=1
	elif [ $phase = 1 ] && grep -q '^push_now ' "$marks"; then
		xcrun simctl push "$udid" "$bundle" "$work/payload.apns"
		phase=2
	elif [ $phase = 2 ] && grep -q '^notification ' "$marks"; then
		sleep 0.6
		xcrun simctl io "$udid" screenshot "$work/lock1.png" >/dev/null 2>&1
		phase=3
	elif [ $phase = 3 ] && grep -q '^unlocked ' "$marks"; then
		sleep 0.5
		record 2
		touch "$ready"
		phase=4
	elif [ $phase = 4 ] && grep -q '^end ' "$marks"; then
		stop 2
		phase=5
	fi
	sleep 0.05
done
[ -n "$rec" ] && { kill -INT $rec; wait $rec || true; rec=""; }
kill $xcb 2>/dev/null || true
if ! grep -q "Test Suite 'Selected tests' passed" "$work/test.log" || [ $phase != 5 ]; then
	echo "the UI test failed; see $work/test.log" >&2
	exit 1
fi
# A recording that froze (a handful of frames) makes a useless take.
if python3 -c "import sys; sys.exit(0 if float(sys.argv[1]) > 8 else 1)" \
	"$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$work/rec2.mp4")"; then :; else
	echo "the recording after the unlock froze; run again" >&2
	exit 1
fi
ffmpeg -loglevel error -y -i "$work/rec2.mp4" -vf fps=30 -c:v libx264 -crf 14 -preset veryfast "$work/cfr2.mp4"
echo "phone take in $work (marks.txt, lock{0,1}.png, rec2.t0, cfr2.mp4)"
