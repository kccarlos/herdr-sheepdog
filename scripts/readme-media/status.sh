#!/usr/bin/env bash
# status.gif: `sheepdog demo` in the browser. The /voice page connects,
# a spoken "What's going on?" (macOS `say`) goes in through the page's
# test-file source, and the voice's answer shows in the transcript.
#
#   scripts/readme-media/status.sh           # writes assets/readme/status.{gif,mp4}
#   RECUT=1 WORK=<dir> scripts/readme-media/status.sh   # re-cut the last take
#
# One take is one short, benign Gemini Live session on the configured key
# (the demo reads ~/.config/sheepdog as `sheepdog demo` does) and one
# orchestrator turn. Needs Go, ffmpeg (gifsicle optional) and a Chromium
# (BROWSER; default Chrome, else Brave), which runs headless with a
# throwaway profile.
#
# The answer's timing varies from take to take: check the contact sheet
# ($WORK/sheet.png) and move the cuts with KEEP ("start-end start-end",
# seconds into the take) if needed.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
. "$here/lib.sh"
work=${WORK:-$(mktemp -d -t sheepdog-status)}
out=$repo/assets/readme
port=${PORT:-7781}
keep=${KEEP:-"0.8-2.8 6.0-13.0"}

if [ -z "${BROWSER:-}" ]; then
	for b in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser" \
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"; do
		[ -x "$b" ] && BROWSER=$b && break
	done
fi

if [ -z "${RECUT:-}" ]; then
	(cd "$repo" && go build -o "$work/sheepdog" ./cmd/sheepdog)
	say -v Samantha -o "$work/q.aiff" "What's going on?"
	ffmpeg -loglevel error -y -i "$work/q.aiff" -af "adelay=200,apad=pad_dur=0.6" -ar 48000 -ac 1 \
		-c:a pcm_s16le "$work/question.wav"

	"$work/sheepdog" demo --no-open --port "$port" --dir "$work/demo" >"$work/demo.out" 2>"$work/demo.err" &
	demo=$!
	trap 'kill -INT $demo 2>/dev/null || true' EXIT
	until curl -sf "http://127.0.0.1:$port/api/state" >/dev/null; do
		kill -0 $demo 2>/dev/null || { cat "$work/demo.err" >&2; exit 1; }
		sleep 0.3
	done

	# Only the controls and the transcript: no stats, no debug link.
	css='main > section.card:not(.controls):not(:has(#transcript)), main > p, .controls .row:not(.buttons),'
	css+=' #aec-hint, #error, header a { display: none !important; }'
	css+=' .transcript { max-height: none !important; font-size: 1.1em; }'
	rm -rf "$work/frames"
	(cd "$repo" && go run scripts/readme-media/cdprec.go -browser "$BROWSER" \
		-url "http://127.0.0.1:$port/voice?src=file&gate=off" -css "$css" \
		-click '#btn-connect' -file "$work/question.wav" -file-at 2.5 \
		-seconds 18 -out "$work/frames") | tee "$work/cdprec.log"
	kill -INT $demo 2>/dev/null || true
	wait $demo 2>/dev/null || true
	trap - EXIT
fi

(cd "$work/frames" && ffmpeg -loglevel error -y -f concat -safe 0 -i frames.txt \
	-vf "fps=30,crop=800:300:0:0" -pix_fmt yuv420p -c:v libx264 -crf 14 "$work/take.mp4")
ffmpeg -loglevel error -y -i "$work/take.mp4" -vf "fps=1,scale=400:-1,tile=4x5" -frames:v 1 "$work/sheet.png"

inputs=()
filter=""
labels=""
i=0
for span in $keep; do
	inputs+=(-ss "${span%-*}" -to "${span#*-}" -i "$work/take.mp4")
	filter+="[$i:v]setpts=PTS-STARTPTS[s$i];"
	labels+="[s$i]"
	i=$((i + 1))
done
filter+="${labels}concat=n=$i:v=1:a=0[v]"
ffmpeg -loglevel error -y "${inputs[@]}" -filter_complex "$filter" -map "[v]" -r 30 "$work/cut.mp4"

mkdir -p "$out"
ffmpeg -loglevel error -y -i "$work/cut.mp4" -c:v libx264 -pix_fmt yuv420p -crf 24 -movflags +faststart -an "$out/status.mp4"
to_gif "$work/cut.mp4" "$out/status.gif" 800 12
echo "work files in $work"
