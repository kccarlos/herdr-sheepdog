#!/usr/bin/env bash
# install.gif: a vhs recording of `sheepdog init` and `sheepdog pair`
# (install.tape) in a throwaway HOME, never the real config.
#
#   scripts/readme-media/install.sh          # writes assets/readme/install.{gif,mp4}
#
# Needs Go, vhs, ffmpeg (gifsicle optional).
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
. "$here/lib.sh"
work=${WORK:-$(mktemp -d -t sheepdog-install)}
out=$repo/assets/readme

mkdir -p "$work/bin"
(cd "$repo" && go build -o "$work/bin/sheepdog" ./cmd/sheepdog)
# A short HOME: herdr's socket path must fit a unix socket address.
home=$(mktemp -d "${TMPDIR:-/tmp}/sdh.XXXX")
home=$(cd "$home" && pwd) # no "//" from a TMPDIR ending in /
mkdir -p "$home/.config/herdr"
# A stand-in for herdr's socket, so init's herdr check passes.
python3 -c '
import socket, sys
s = socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(8)
while True:
    s.accept()[0].close()
' "$home/.config/herdr/herdr.sock" &
sock=$!
trap 'kill $sock 2>/dev/null; rm -rf "$home"' EXIT

cp "$here/install.tape" "$work/"
(cd "$work" && SDHOME=$home SDBIN=$work/bin vhs install.tape)

mkdir -p "$out"
ffmpeg -loglevel error -y -i "$work/install.mp4" -c:v libx264 -pix_fmt yuv420p -crf 24 -movflags +faststart -an "$out/install.mp4"
to_gif "$work/install.mp4" "$out/install.gif" 800 12
echo "work files in $work"
