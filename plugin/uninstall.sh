#!/bin/sh
# Reverses install.sh; idempotent and safe even if install.sh was never
# run. SHEEPDOG_DRY_RUN=1 prints instead of running.
set -eu
DIR=$(cd "$(dirname "$0")" && pwd)
. "$DIR/lib.sh"

if [ "$(uname -s)" = "Darwin" ]; then
  LABEL="gui/$(id -u)/dev.sheepdog.daemon"
  PLIST="$HOME/Library/LaunchAgents/dev.sheepdog.daemon.plist"
  if launchctl print "$LABEL" >/dev/null 2>&1; then
    echo "uninstall.sh: unloading $LABEL"
    run launchctl bootout "$LABEL"
  fi
  if [ -f "$PLIST" ]; then
    echo "uninstall.sh: removing $PLIST"
    run rm -f "$PLIST"
  fi
fi

echo "uninstall.sh: herdr plugin unlink sheepdog.herdr-shim"
run herdr plugin unlink sheepdog.herdr-shim || true
