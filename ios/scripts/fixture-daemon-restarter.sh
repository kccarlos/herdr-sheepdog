#!/usr/bin/env bash
# Restarts the fixture daemon on 127.0.0.1:$PORT (default 7799) whenever ReconnectUITests
# asks (by creating $CONTROL/restart): kill it, wait 3 s, start it again.
# The simulator shares the Mac's file system, which is the only channel a
# UI test has to the host. Only ever touches the listener on $PORT.
#
#   SHEEPDOG_DAEMON_REPO=~/Documents/herdr-sheepdog scripts/fixture-daemon-restarter.sh
#   (SHEEPDOG_FIXTURE_PORT, SHEEPDOG_CONTROL_DIR and SHEEPDOG_FIXTURE_DATA move it
#   off 7799 and /tmp/sd-bugs; pass the same port and control directory to the
#   tests as TEST_RUNNER_SHEEPDOG_FIXTURE_PORT / TEST_RUNNER_SHEEPDOG_CONTROL_DIR.)
set -u
CONTROL=${SHEEPDOG_CONTROL_DIR:-/tmp/sd-bugs/control}
DAEMON_REPO=${SHEEPDOG_DAEMON_REPO:?set SHEEPDOG_DAEMON_REPO to the daemon checkout}
DATA=${SHEEPDOG_FIXTURE_DATA:-/tmp/sd-bugs}
PORT=${SHEEPDOG_FIXTURE_PORT:-7799}
mkdir -p "$CONTROL" "$DATA"
[ -e /tmp/sd-empty.toml ] || printf '' > /tmp/sd-empty.toml

start_daemon() {
  (cd "$DAEMON_REPO" && SHEEPDOG_DATA_DIR="$DATA" SHEEPDOG_STATE_PATH="$DATA/state.json" \
    nohup bin/sheepdog serve --config /tmp/sd-empty.toml --brain=fake --herdr=mock \
      --voice=off --cli=off --dev --port "$PORT" --env-file /dev/null >>"$DATA/daemon.log" 2>&1 &)
}

while true; do
  if [ -e "$CONTROL/restart" ]; then
    rm -f "$CONTROL/restart" "$CONTROL/restarted"
    pid=$(lsof -tiTCP:"$PORT" -sTCP:LISTEN || true)
    [ -n "$pid" ] && kill "$pid"
    echo "killed fixture daemon ($pid); restarting in 3 s"
    sleep 3
    start_daemon
    touch "$CONTROL/restarted"
  fi
  sleep 0.2
done
