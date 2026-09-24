#!/usr/bin/env bash
# Restarts the fixture daemon on 127.0.0.1:7799 whenever ReconnectUITests
# asks (by creating $CONTROL/restart): kill it, wait 3 s, start it again.
# The simulator shares the Mac's file system, which is the only channel a
# UI test has to the host. Only ever touches the listener on port 7799.
#
#   SHEEPDOG_DAEMON_REPO=~/Documents/herdr-sheepdog scripts/fixture-daemon-restarter.sh
set -u
CONTROL=${SHEEPDOG_CONTROL_DIR:-/tmp/sd-bugs/control}
DAEMON_REPO=${SHEEPDOG_DAEMON_REPO:?set SHEEPDOG_DAEMON_REPO to the daemon checkout}
DATA=${SHEEPDOG_FIXTURE_DATA:-/tmp/sd-bugs}
mkdir -p "$CONTROL" "$DATA"
[ -e /tmp/sd-empty.toml ] || printf '' > /tmp/sd-empty.toml

start_daemon() {
  (cd "$DAEMON_REPO" && SHEEPDOG_DATA_DIR="$DATA" SHEEPDOG_STATE_PATH="$DATA/state.json" \
    nohup bin/sheepdog serve --config /tmp/sd-empty.toml --brain=fake --herdr=mock \
      --voice=off --cli=off --dev --port 7799 --env-file /dev/null >>"$DATA/daemon.log" 2>&1 &)
}

while true; do
  if [ -e "$CONTROL/restart" ]; then
    rm -f "$CONTROL/restart" "$CONTROL/restarted"
    pid=$(lsof -tiTCP:7799 -sTCP:LISTEN || true)
    [ -n "$pid" ] && kill "$pid"
    echo "killed fixture daemon ($pid); restarting in 3 s"
    sleep 3
    start_daemon
    touch "$CONTROL/restarted"
  fi
  sleep 0.2
done
