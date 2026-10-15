#!/usr/bin/env bash
# The launch video: see README.md.
#   IOS_REPO=/path/to/herdr-sheepdog-ios scripts/readme-media/launch/make.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
export WORK=${WORK:-$(mktemp -d -t sheepdog-launch)}
for step in ${STEPS:-"tts phone compose"}; do
	case $step in
	tts) (cd "$repo" && go run scripts/readme-media/hero/tts.go -lines "$here/lines.tsv" -out "$WORK/tts") ;;
	phone) "$here/phone.sh" ;;
	compose)
		[ -f "$WORK/logo.png" ] || { qlmanage -t -s 600 -o "$WORK" "$repo/assets/logo-transparent.svg" >/dev/null 2>&1; mv "$WORK/logo-transparent.svg.png" "$WORK/logo.png"; }
		uv run -q --with pillow python "$here/compose.py" "$WORK" "$repo/assets/readme" ;;
	esac
done
echo "work files in $WORK"
