#!/usr/bin/env bash
# phone.gif: the iOS app's offline demo in the iOS Simulator.
#
# Runs the iOS repo's ReadmeRecordingUITests (it drives "Try the demo" →
# the four agents → billing's approval → Approve → "What failed?" → the
# answer, and writes a timestamp per step) while `simctl io recordVideo`
# films the simulator, then cuts the scripted waits out with ffmpeg.
#
#   scripts/readme-media/phone.sh            # writes assets/readme/phone.{gif,mp4}
#   IOS_REPO=/path/to/herdr-sheepdog-ios scripts/readme-media/phone.sh
#
# Needs Xcode, xcodegen, ffmpeg (gifsicle optional). Uses its own simulator
# ("Sheepdog README", an iPhone 18 Pro by default; DEVICE_TYPE overrides)
# in light mode with a 9:41 status bar, so it never touches another one.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
. "$here/lib.sh"
ios=${IOS_REPO:-$repo/../herdr-sheepdog-ios}
ios=$(cd "$ios" && pwd)
device_type=${DEVICE_TYPE:-iPhone 18 Pro}
name="Sheepdog README"
bundle=com.github.kccarlos.herdr-sheepdog-ios
work=${WORK:-$(mktemp -d -t sheepdog-phone)}
out=$repo/assets/readme

udid=$(xcrun simctl list devices available | awk -v n="$name" -F '[()]' 'index($0, n" (") {print $2; exit}')
if [ -z "$udid" ]; then
	udid=$(xcrun simctl create "$name" "$device_type")
fi
xcrun simctl boot "$udid" 2>/dev/null || true
xcrun simctl bootstatus "$udid" -b >/dev/null
xcrun simctl ui "$udid" appearance light
xcrun simctl status_bar "$udid" override --time 9:41 --batteryState charged --batteryLevel 100 \
	--cellularBars 4 --wifiBars 3 --dataNetwork wifi
# Answer the microphone question up front (the UI test's warm-up answers
# speech recognition) and skip the keyboard's swipe-typing tip: no
# alert or tip in the video.
xcrun simctl privacy "$udid" grant microphone "$bundle" 2>/dev/null || true
xcrun simctl spawn "$udid" defaults write com.apple.keyboard.preferences DidShowContinuousPathIntroduction -bool true
xcrun simctl spawn "$udid" defaults write com.apple.keyboard.preferences DidShowGestureKeyboardIntroduction -bool true

if [ -z "${SKIP_BUILD:-}" ]; then
	(cd "$ios" && xcodegen generate >/dev/null)
	xcodebuild build-for-testing -project "$ios/Sheepdog.xcodeproj" -scheme Sheepdog \
		-destination "id=$udid" -derivedDataPath "$work/dd" CODE_SIGNING_ALLOWED=NO >"$work/build.log" 2>&1
fi
xctestrun=$(ls "$work"/dd/Build/Products/*.xctestrun | head -1)

marks=$work/marks.txt
if [ -n "${RECUT:-}" ]; then
	# RECUT=1: cut the last take again (same WORK) without recording.
	t0=$(cat "$work/t0")
else
rm -f "$marks" "$work/raw.mp4"
xcrun simctl io "$udid" recordVideo --codec=h264 --force "$work/raw.mp4" 2>"$work/rec.log" &
rec=$!
until grep -q "Recording started" "$work/rec.log" 2>/dev/null; do
	if ! kill -0 $rec 2>/dev/null; then
		cat "$work/rec.log" >&2 # e.g. a stale recording: xcrun simctl shutdown "$name"
		exit 1
	fi
	sleep 0.05
done
t0=$(python3 -c 'import time; print(time.time())')

TEST_RUNNER_SHEEPDOG_README_MARKS=$marks xcodebuild test-without-building -xctestrun "$xctestrun" \
	-destination "id=$udid" -only-testing:SheepdogUITests/ReadmeRecordingUITests >"$work/test.log" 2>&1 &
xcb=$!
# Stop the recording as soon as the test is over (xcodebuild sometimes
# lingers for minutes after the summary).
while kill -0 $xcb 2>/dev/null && ! grep -qE "Test Suite 'Selected tests' (passed|failed)" "$work/test.log"; do
	sleep 0.2
done
t1=$(python3 -c 'import time; print(time.time())')
kill -INT $rec
wait $rec || true
kill $xcb 2>/dev/null || true
if ! grep -q "Test Suite 'Selected tests' passed" "$work/test.log"; then
	echo "the UI test failed; see $work/test.log" >&2
	exit 1
fi
# The video's clock: simctl writes a frame only when the screen changes,
# so its first frame can lag "Recording started" by seconds. Its end is
# the stop, so count back from there.
dur=$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$work/raw.mp4")
echo "start by log $t0, by end $(python3 -c "print($t1 - $dur)")" >&2
t0=$(python3 -c "print($t1 - $dur)")
echo "$t0" >"$work/t0"

fi

# Seconds into the video for each step.
at() { awk -v s="$1" -v t0="$t0" '$1 == s {printf "%.2f", $2 - t0}' "$marks"; }
# The kept stretches: entering the demo; the agents; billing's
# approval being answered; the conversation (migrate's failure), the
# question and its answer.
segs=(
	"$(at tap_demo) - 1.2" "$(at demo_open) + 0.5"
	"$(at agents) - 0.3" "$(at agents) + 1.2"
	"$(at approval) - 0.2" "$(at tap_approve) + 1.6"
	"$(at conversation) + 0.2" "$(at answer) + 2.2"
)
# simctl writes a frame only when the screen changes: make the video
# constant-rate first, or a cut that starts on a still screen loses it.
ffmpeg -loglevel error -y -i "$work/raw.mp4" -vf fps=30 -c:v libx264 -crf 12 -preset veryfast "$work/cfr.mp4"
inputs=()
filter=""
for i in 0 1 2 3; do
	a=$(python3 -c "print(max(0, ${segs[$((2 * i))]}))")
	b=$(python3 -c "print(${segs[$((2 * i + 1))]})")
	inputs+=(-ss "$a" -to "$b" -i "$work/cfr.mp4")
	filter+="[$i:v]setpts=PTS-STARTPTS[s$i];"
done
# Played a little fast (SPEED, default 1.25) to stay near 10 s.
filter+="[s0][s1][s2][s3]concat=n=4:v=1:a=0,setpts=PTS/${SPEED:-1.25}[v]"
ffmpeg -loglevel error -y "${inputs[@]}" -filter_complex "$filter" -map "[v]" -r 30 "$work/cut.mp4"

mkdir -p "$out"
# Portrait: about 800 px tall.
ffmpeg -loglevel error -y -i "$work/cut.mp4" -vf "scale=-2:800:flags=lanczos" -c:v libx264 -pix_fmt yuv420p \
	-crf 26 -movflags +faststart -an "$out/phone.mp4"
to_gif "$work/cut.mp4" "$out/phone.gif" 368 12
echo "work files in $work"
