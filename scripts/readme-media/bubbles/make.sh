#!/usr/bin/env bash
# The README's hero GIF (12 s, looping): the real iPhone app on the left,
# the conversation as chat bubbles on the right, then the end tag.
#
#   scripts/readme-media/bubbles/make.sh
#   STEPS=compose WORK=<dir from the last run> scripts/readme-media/bubbles/make.sh   # re-draw only
#
# The phone take is the hero video's (scripts/readme-media/hero/phone.sh:
# the iOS app's offline demo in the Simulator, driven by
# HeroRecordingUITests; the lock-screen notification is sent with `xcrun
# simctl push` and the daemon's own payload). The bubbles are lines.tsv,
# the demo world's exact lines; edit it and re-draw with STEPS=compose.
# Writes assets/readme/hero.gif (800x450, 15 fps) and hero-loop.mp4
# (1600x900). Needs what phone.sh needs, plus ffmpeg, gifsicle and uv.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
export WORK=${WORK:-$(mktemp -d -t sheepdog-bubbles)}
steps=${STEPS:-"phone compose"}

for step in $steps; do
	case $step in
	phone) "$repo/scripts/readme-media/hero/phone.sh" ;;
	compose)
		# The logo as a PNG (Quick Look renders the SVG).
		if [ ! -f "$WORK/logo.png" ]; then
			qlmanage -t -s 600 -o "$WORK" "$repo/assets/logo-transparent.svg" >/dev/null 2>&1
			mv "$WORK/logo-transparent.svg.png" "$WORK/logo.png"
		fi
		uv run -q --with pillow python "$here/bubbles.py" "$WORK" "$WORK/logo.png" "$repo/assets/readme"
		cat "$WORK/bubbles/timing.txt"
		;;
	esac
done
echo "work files in $WORK"
