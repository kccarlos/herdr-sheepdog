#!/usr/bin/env bash
# status.gif: `sheepdog demo` in the browser, at the moment an agent
# needs you. The demo starts its workers at once (--start-now); the
# /voice page connects ~20 s in, hears that docs finished, then (45 s in)
# that the tests worker asks for approval. Once that is said, a spoken
# "What's going on?" (macOS `say`) goes in through the page's test-file
# source, the answer (which should lead with the approval) shows in the
# transcript, and a spoken "Approve it." should get the confirmation. The
# cut keeps the approval and what follows. (In the committed take the
# answer came in two parts and "Approve it." landed in the pause between
# them, so the voice never took it; the GIF ends on the answer. -hold2 is
# now longer to wait out that pause.)
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
keep=${KEEP:-"26.6-30.2 34.4-40.2 49.5-54"}
crop=${CROP:-800:470:0:0}
# Seconds between the demo's start and the recording's start (cdprec
# clicks Connect ~3.5 s after it starts): after docs finishes (25 s) would
# make the voice say that news on connect; the tests worker blocks at 45 s.
wait_s=${WAIT:-18}
# The questions go in once the voice has finished saying what they answer.
last='[...document.querySelectorAll("#transcript .line")].pop()'
quiet='document.getElementById("speaking-flag").classList.contains("hidden")'
q_when="$quiet && (l => l && !l.classList.contains('in') && /test/i.test(l.textContent) && /approv|go-ahead|proceed|permission|waiting/i.test(l.textContent))($last)"
a_when="$quiet && document.querySelector('#transcript .line.in') && (l => l && !l.classList.contains('in'))($last)"

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
	say -v Samantha -o "$work/a.aiff" "Approve it."
	for f in q:question a:approve; do
		ffmpeg -loglevel error -y -i "$work/${f%%:*}.aiff" -af "adelay=200,apad=pad_dur=0.6" -ar 48000 -ac 1 \
			-c:a pcm_s16le "$work/${f#*:}.wav"
	done

	"$work/sheepdog" demo --no-open --start-now --port "$port" --dir "$work/demo" >"$work/demo.out" 2>"$work/demo.err" &
	demo=$!
	trap 'kill -INT $demo 2>/dev/null || true' EXIT
	until curl -sf "http://127.0.0.1:$port/api/state" >/dev/null; do
		kill -0 $demo 2>/dev/null || { cat "$work/demo.err" >&2; exit 1; }
		sleep 0.3
	done
	started=$(date +%s)

	# Only the controls and the transcript: no stats, no debug link.
	css='main > section.card:not(.controls):not(:has(#transcript)), main > p, .controls .row:not(.buttons),'
	css+=' #aec-hint, #error, header a { display: none !important; }'
	css+=' .transcript { max-height: none !important; font-size: 1.1em; }'
	css+=' #transcript .line:nth-last-child(n+4) { display: none; }'
	rm -rf "$work/frames"
	sleep $((wait_s - ($(date +%s) - started)))
	(cd "$repo" && go run scripts/readme-media/cdprec.go -browser "$BROWSER" \
		-url "http://127.0.0.1:$port/voice?src=file&gate=off" -css "$css" \
		-click '#btn-connect' -file "$work/question.wav" -file-at 2.5 -file-when "$q_when" -hold 1.2 \
		-file2 "$work/approve.wav" -file2-at 2.5 -file2-when "$a_when" -hold2 5 \
		-after2 10 -seconds 90 -out "$work/frames") | tee "$work/cdprec.log"
	kill -INT $demo 2>/dev/null || true
	wait $demo 2>/dev/null || true
	trap - EXIT
fi

(cd "$work/frames" && ffmpeg -loglevel error -y -f concat -safe 0 -i frames.txt \
	-vf "fps=30,crop=$crop" -pix_fmt yuv420p -c:v libx264 -crf 14 "$work/take.mp4")
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
