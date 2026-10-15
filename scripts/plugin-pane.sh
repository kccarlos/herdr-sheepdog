#!/bin/sh
# Pane command of the herdr plugin: runs sheepdog with the given arguments
# inside a popup, then waits for Enter. A popup closes when its command
# exits, which would wipe the QR code or the doctor report before it
# could be read. Not used for `service logs -f`, which stays open itself.
set -eu

bin=$(dirname "$0")/../bin/sheepdog
[ -x "$bin" ] || {
	echo "sheepdog is not installed: run scripts/plugin-fetch.sh in the plugin directory." >&2
	printf '\nPress Enter to close. '
	read -r _
	exit 1
}
status=0
"$bin" "$@" || status=$?
printf '\nPress Enter to close. '
read -r _
exit "$status"
