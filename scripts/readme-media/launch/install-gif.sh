#!/usr/bin/env bash
# assets/readme/install.gif: the new install flow, about 15 s at 800 px.
#
#   scripts/readme-media/launch/install-gif.sh        # writes assets/readme/install.gif
#
# Terminal half (vhs, install.tape): `herdr plugin install
# kccarlos/herdr-sheepdog`, then `sheepdog setup`. There is no GitHub
# release to install from yet, so `herdr plugin install` is a shell
# function in the throwaway shell that runs the plugin's real build step
# (scripts/plugin-fetch.sh, against a local release folder with a real
# checksums.txt) and prints what that script prints. Everything else is the
# real binary.
# Browser half (cdprec.go, headless Chrome/Brave): the real /setup page
# of the daemon `sheepdog setup` just started, in a throwaway HOME on
# port $PORT (never 7777), setup-page.js driving it: a placeholder key is
# typed, the daemon validates the real key from $GEMINI_API_KEY (not shown),
# the checks run, phone access is turned on, and the QR on screen is a
# dummy one for 192.168.1.20. No login service is installed.
# Needs Go, vhs, ffmpeg, gifsicle, uv (Pillow), a Chromium browser and
# $GEMINI_API_KEY (the key only validates; the page never shows it).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
. "$here/../lib.sh"
work=${WORK:-$(mktemp -d -t sheepdog-install-gif)}
port=${PORT:-7790}
browser=${BROWSER_BIN:-/Applications/Brave Browser.app/Contents/MacOS/Brave Browser}
[ -x "$browser" ] || browser="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
: "${GEMINI_API_KEY:?set GEMINI_API_KEY (it validates the key behind the placeholder; never shown)}"
[ "$port" != 7777 ] && [ "$port" != 7778 ] || { echo "not the live daemon's port" >&2; exit 1; }

mkdir -p "$work/bin"
# Built the way a release is (a version in `sheepdog --version`).
(cd "$repo" && go build -ldflags "-X 'main.version=v0.1.0' -X 'main.commit=$(git rev-parse --short HEAD)' -X 'main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)'" -o "$work/bin/sheepdog" ./cmd/sheepdog)

# A short HOME (herdr's socket path must fit a unix socket address).
home=$(mktemp -d "${TMPDIR:-/tmp}/sdi.XXXX")
home=$(cd "$home" && pwd)
cleanup() {
	if [ -n "${sock:-}" ]; then kill "$sock" 2>/dev/null || true; fi
	HOME=$home PATH="$home/.local/bin:/usr/bin:/bin" "$work/bin/sheepdog" stop >/dev/null 2>&1 || true
	rm -rf "$home" "$work/setup-page.js"
}
trap cleanup EXIT

# The plugin root the build step writes into (a copy: the repo's bin/ is
# left alone), and a local "release" for it to download.
plugin=$home/herdr-sheepdog
mkdir -p "$plugin/scripts" "$home/.config/herdr" "$home/.config/sheepdog" "$home/stubs" "$home/release" "$home/.local/bin"
cp "$repo/scripts/plugin-fetch.sh" "$plugin/scripts/"
case "$(uname -s)_$(uname -m)" in
Darwin_arm64) target=darwin_arm64 ;;
Linux_x86_64) target=linux_amd64 ;;
*) echo "no release target for this machine" >&2; exit 1 ;;
esac
asset=sheepdog_v0.1.0_$target.tar.gz
tar -czf "$home/release/$asset" -C "$work/bin" sheepdog
(cd "$home/release" && shasum -a 256 "$asset" >checksums.txt)
# A stand-in for herdr's socket, so the herdr check passes.
python3 -c '
import socket, sys
s = socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(8)
while True:
    s.accept()[0].close()
' "$home/.config/herdr/herdr.sock" &
sock=$!
# `sheepdog setup` opens a browser; the recording opens the page itself.
for b in open xdg-open; do printf '#!/bin/sh\nexit 0\n' >"$home/stubs/$b"; chmod +x "$home/stubs/$b"; done
printf 'data_dir = "%s/data"\n[web]\nport = %s\n' "$home" "$port" >"$home/.config/sheepdog/config.toml"

# --- terminal half
cp "$here/install.tape" "$work/"
(cd "$work" && env -i HOME="$HOME" PATH="$PATH" TERM=xterm-256color \
	SDHOME="$home" SDPATH="$home/stubs:$home/.local/bin:/usr/bin:/bin" \
	SHEEPDOG_RELEASE_URL="file://$home/release" vhs install.tape)
curl -sf "http://127.0.0.1:$port/api/state" >/dev/null || { echo "setup did not start the daemon on :$port" >&2; exit 1; }

# --- browser half
qr=$(cd "$repo" && go run scripts/readme-media/launch/dummyqr.go | base64 | tr -d '\n')
python3 -I - "$here/setup-page.js" "$work/setup-page.js" "$qr" <<'PY'
import os, sys
src, dst, qr = sys.argv[1:4]
s = open(src).read().replace("__REAL_KEY__", os.environ["GEMINI_API_KEY"]).replace("__QR_SVG__", qr)
fd = os.open(dst, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w") as f:
    f.write(s)
PY
printf '0.2\twindow.__demo()\n' >"$work/steps.txt"
rm -rf "$work/frames"
(cd "$repo" && go run scripts/readme-media/cdprec.go -browser "$browser" -url "http://127.0.0.1:$port/setup" \
	-out "$work/frames" -seconds "${SECONDS_MAX:-60}" -width 800 -height 560 -port 9344 \
	-init-js "$work/setup-page.js" -steps "$work/steps.txt" -done-when 'window.__done === true' | grep -v "^transcript" | head -20)
rm -f "$work/setup-page.js"

# --- compose: both halves at 800x560, waits shortened, one GIF
python3 "$here/install-compose.py" "$work/terminal.mp4" "$work/frames" "$work/install-src.mp4"
mkdir -p "$repo/assets/readme"
to_gif "$work/install-src.mp4" "$repo/assets/readme/install.gif" 800 12
ffprobe -v error -show_entries format=duration -of csv=p=0 "$repo/assets/readme/install.gif" 2>/dev/null || true
echo "work files in $work"
