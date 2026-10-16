#!/usr/bin/env bash
# The hero video, end to end: the phone take (phone.sh), the desk takes
# (desk.sh), the voice lines (tts.go) and the cut (compose.py), into
# assets/readme/hero.mp4 and hero-vertical.mp4 (the README GIF: ../bubbles/).
#
#   scripts/readme-media/hero/make.sh
#   STEPS="compose" WORK=<dir> scripts/readme-media/hero/make.sh   # re-cut only
#
# Needs Xcode + xcodegen, Go, herdr, vhs, ffmpeg, gifsicle, uv. See README.md.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
export WORK=${WORK:-$(mktemp -d -t sheepdog-hero)}
steps=${STEPS:-"phone desk voice compose"}

for step in $steps; do
	case $step in
	phone) "$here/phone.sh" ;;
	desk) "$here/desk.sh" ;;
	voice) (cd "$repo" && go run scripts/readme-media/hero/tts.go -lines "$here/lines.tsv" -out "$WORK/voice" ${TTS_SAY:+-say}) ;;
	compose)
		# The logo as a PNG (Quick Look renders the SVG).
		if [ ! -f "$WORK/logo.png" ]; then
			qlmanage -t -s 600 -o "$WORK" "$repo/assets/logo-transparent.svg" >/dev/null 2>&1
			mv "$WORK/logo-transparent.svg.png" "$WORK/logo.png"
		fi
		uv run -q --with pillow python "$here/compose.py" "$WORK" "$repo/assets/readme"
		;;
	esac
done
ls -l "$repo"/assets/readme/hero*
echo "work files in $WORK"
