#!/usr/bin/env bash
# The launch video: see README.md.
#   SIM=<udid> op run --env-file=.env -- scripts/readme-media/launch/make.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
export WORK=${WORK:-$(mktemp -d -t sheepdog-launch)}
for step in ${STEPS:-"tts phone compose"}; do
	case $step in
	tts) (cd "$repo" && go run scripts/readme-media/hero/tts.go -strict -lines "$here/lines.tsv" -out "$WORK/tts") ;;
	phone) "$here/phone.sh" ;;
	compose) uv run -q --with pillow --with fonttools --with brotli python "$here/compose.py" "$WORK" "$repo/assets/readme" ;;
	esac
done
echo "work files in $WORK"
