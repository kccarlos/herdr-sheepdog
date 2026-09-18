#!/bin/sh
# Idempotent: links (or, given a GitHub owner/repo, installs) the
# sheepdog herdr plugin shim, then on macOS installs and (re)starts the
# sheepdog launchd agent. SHEEPDOG_DRY_RUN=1 prints every mutating
# command instead of running it. Never touches herdr/launchd for real
# under that flag.
set -eu
DIR=$(cd "$(dirname "$0")" && pwd)
. "$DIR/lib.sh"

if [ "${1:-}" ]; then
  echo "install.sh: herdr plugin install $1"
  run herdr plugin install "$1" --yes
else
  echo "install.sh: herdr plugin link $DIR"
  run herdr plugin link "$DIR"
fi

if [ "$(uname -s)" != "Darwin" ]; then
  echo "install.sh: not macOS, skipping the launchd agent (run 'sheepdog serve' yourself)"
  exit 0
fi

BIN=${SHEEPDOG_BIN:-$(command -v sheepdog || echo "$DIR/../bin/sheepdog")}
LOG_DIR=${SHEEPDOG_LOG_DIR:-"$HOME/Library/Logs/sheepdog"}
PORT=${SHEEPDOG_PORT:-7777}
PLIST_DIR="$HOME/Library/LaunchAgents"
PLIST="$PLIST_DIR/dev.sheepdog.daemon.plist"
LABEL="gui/$(id -u)/dev.sheepdog.daemon"

echo "install.sh: binary=$BIN log_dir=$LOG_DIR port=$PORT plist=$PLIST"
TMP=$(mktemp)
sed -e "s#__BINARY__#$BIN#g" -e "s#__LOG_DIR__#$LOG_DIR#g" -e "s#__PORT__#$PORT#g" \
  "$DIR/launchd/dev.sheepdog.daemon.plist.tmpl" >"$TMP"
run mkdir -p "$LOG_DIR" "$PLIST_DIR"
run cp "$TMP" "$PLIST"
rm -f "$TMP"

if launchctl print "$LABEL" >/dev/null 2>&1; then
  echo "install.sh: $LABEL already loaded, restarting"
  run launchctl kickstart -k "$LABEL"
else
  echo "install.sh: bootstrapping $LABEL"
  run launchctl bootstrap "gui/$(id -u)" "$PLIST"
  run launchctl kickstart "$LABEL"
fi
echo "install.sh: done. status: launchctl print $LABEL"
