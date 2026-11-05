#!/usr/bin/env bash
# setup.gif: the web setup page that `sheepdog setup` opens, on a first run:
# the voice step with a (fake) key, then the pairing QR.
#
#   scripts/readme-media/setup.sh            # writes assets/readme/setup.gif
#
# It builds the daemon from this checkout and runs it privately (loopback
# port 18001, a throwaway home with no keys and a mock herdr, nothing of the
# real ~/.config/sheepdog). setup-shots.go drives the page in a headless
# Chromium with a throwaway profile and answers the key check and the QR
# itself: the key is fake, nothing is saved, and the QR is not a real pairing
# code. Needs Go, ffmpeg (gifsicle optional) and a Chromium (BROWSER; default
# Chrome, else Brave, else Edge).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
. "$here/lib.sh"
work=${WORK:-$(mktemp -d -t sheepdog-setup)}
out=$repo/assets/readme
port=${PORT:-18001}

if [ -z "${BROWSER:-}" ]; then
	for b in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" \
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser" \
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"; do
		[ -x "$b" ] && BROWSER=$b && break
	done
fi
[ -n "${BROWSER:-}" ] || { echo "no Chromium found: set BROWSER" >&2; exit 1; }
[ "$port" != 7777 ] || { echo "port 7777 is the real daemon's" >&2; exit 1; }

mkdir -p "$work"/{home,xdg/config,xdg/data,xdg/state,xdg/cache,data}
(cd "$repo" && go build -o "$work/sheepdog" ./cmd/sheepdog)
# env -i: no API key of the caller's reaches the daemon, so the page is a first run.
env -i HOME="$work/home" XDG_CONFIG_HOME="$work/xdg/config" XDG_DATA_HOME="$work/xdg/data" \
	XDG_STATE_HOME="$work/xdg/state" XDG_CACHE_HOME="$work/xdg/cache" PATH=/usr/bin:/bin:/usr/sbin:/sbin \
	"$work/sheepdog" serve --herdr=mock --voice=off --cli=off --dev --port "$port" --data-dir "$work/data" \
	</dev/null >"$work/daemon.log" 2>&1 &
pid=$!
trap 'kill "$pid" 2>/dev/null || true' EXIT
for _ in $(seq 1 40); do
	curl -fs --max-time 2 "http://127.0.0.1:$port/api/state" >/dev/null && break
	sleep 0.5
done

(cd "$repo" && go run scripts/readme-media/setup-shots.go -browser "$BROWSER" -url "http://127.0.0.1:$port/setup" -out "$work/shots")

# Each step is one frame of the same size.
ffmpeg -loglevel error -y \
	-loop 1 -t 2.5 -i "$work/shots/1-models.png" -loop 1 -t 3 -i "$work/shots/2-key.png" -loop 1 -t 4 -i "$work/shots/3-pair.png" \
	-filter_complex "[0][1][2]concat=n=3:v=1,fps=10[v]" -map "[v]" "$work/setup.mp4"
w=$(ffprobe -v error -select_streams v:0 -show_entries stream=width -of csv=p=0 "$work/setup.mp4")
to_gif "$work/setup.mp4" "$out/setup.gif" "$w" 10
