#!/usr/bin/env bash
# Restarts the main fixture daemon whenever ReconnectUITests asks (by
# creating $CONTROL/restart): `fixture-daemons.sh restart` with a 3 s gap,
# then $CONTROL/restarted once the daemon answers again. The simulator
# shares the Mac's file system, which is the only channel a UI test has to
# the host. Takes the same environment as fixture-daemons.sh (port, data
# directory), plus SHEEPDOG_CONTROL_DIR (default /tmp/sd-bugs/control); pass
# the same control directory to the tests as TEST_RUNNER_SHEEPDOG_CONTROL_DIR.
#
#   SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemon-restarter.sh
set -u
CONTROL=${SHEEPDOG_CONTROL_DIR:-/tmp/sd-bugs/control}
: "${SHEEPDOG_DAEMON_REPO:?set SHEEPDOG_DAEMON_REPO to the daemon checkout}"
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$CONTROL"

while true; do
  if [ -e "$CONTROL/restart" ]; then
    rm -f "$CONTROL/restart" "$CONTROL/restarted"
    echo "restarting the fixture daemon with a 3 s gap"
    SHEEPDOG_RESTART_GAP=3 "$here/fixture-daemons.sh" restart && touch "$CONTROL/restarted"
  fi
  sleep 0.2
done
