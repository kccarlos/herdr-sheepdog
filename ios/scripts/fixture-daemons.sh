#!/usr/bin/env bash
# Starts, restarts and stops the two fixture daemons the UI tests talk to
# (ios/docs/dev/status.md, "Running the tests"). ios.yml runs it on the
# GitHub-hosted Mac; it works the same on yours.
#
#   SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemons.sh start    # both, plus a restart divider
#   SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemons.sh restart  # the main one only
#   SHEEPDOG_DAEMON_REPO=$PWD ios/scripts/fixture-daemons.sh stop
#
# `restart` waits SHEEPDOG_RESTART_GAP seconds (default 0) between the stop
# and the start; scripts/fixture-daemon-restarter.sh restarts through it, so
# a restart by either gives the same daemon. The main daemon listens on
# SHEEPDOG_FIXTURE_PORT (default 7799). The pair daemon
# (SHEEPDOG_FIXTURE_PAIR_PORT, default 7798) listens on every address and
# lists 127.0.0.1 in its pairing code (web.advertise), so a test can pair by
# code over TLS on this Mac. Data, state, logs, the config and the
# daemons' HOME go under SHEEPDOG_FIXTURE_DATA (default /tmp/sd-bugs), so
# they never read this Mac's ~/.config/sheepdog (config, prompts) or its
# herdr socket. Pass the same ports to the tests as
# TEST_RUNNER_SHEEPDOG_FIXTURE_PORT and TEST_RUNNER_SHEEPDOG_FIXTURE_PAIR_PORT.
# Neither daemon needs a model key: --brain=fake is a scripted model and
# --voice=off has no live session. Needs bin/sheepdog in
# SHEEPDOG_DAEMON_REPO (make build).
set -euo pipefail

REPO=${SHEEPDOG_DAEMON_REPO:?set SHEEPDOG_DAEMON_REPO to the daemon checkout}
PORT=${SHEEPDOG_FIXTURE_PORT:-7799}
PAIR_PORT=${SHEEPDOG_FIXTURE_PAIR_PORT:-7798}
DATA=${SHEEPDOG_FIXTURE_DATA:-/tmp/sd-bugs}
BIN="$REPO/bin/sheepdog"
# Every per-user default (prompts.dir, herdr.socket, the project roots)
# resolves under this HOME instead of the real one.
FIXTURE_HOME="$DATA/home"
# The defaults, under FIXTURE_HOME, but no update check and no restart
# into a rebuilt bin/sheepdog in the middle of a test run.
CONFIG="$DATA/config.toml"

[ -x "$BIN" ] || { echo "fixture-daemons: no $BIN; run make build" >&2; exit 2; }
mkdir -p "$DATA/pair" "$FIXTURE_HOME"
printf '[update]\ncheck = false\nauto_restart = false\n' > "$CONFIG"

# wait_up <port>: until /api/hello answers (it is public).
wait_up() {
  local i
  for i in $(seq 1 100); do
    if curl -fs -o /dev/null --max-time 2 "http://127.0.0.1:$1/api/hello"; then return 0; fi
    sleep 0.2
  done
  echo "fixture-daemons: nothing answers on 127.0.0.1:$1" >&2
  tail -n 40 "$DATA"/*.log "$DATA"/pair/*.log 2>/dev/null >&2 || true
  return 1
}

# wait_down <port>: until nothing listens there.
wait_down() {
  local i
  for i in $(seq 1 100); do
    lsof -tiTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1 || return 0
    sleep 0.2
  done
  echo "fixture-daemons: 127.0.0.1:$1 is still taken" >&2
  return 1
}

start_main() {
  (cd "$REPO" || exit 1; HOME="$FIXTURE_HOME" SHEEPDOG_DATA_DIR="$DATA" SHEEPDOG_STATE_PATH="$DATA/state.json" \
    nohup "$BIN" serve --config "$CONFIG" --brain=fake --herdr=mock \
      --voice=off --cli=off --dev --port "$PORT" --env-file /dev/null \
      >>"$DATA/daemon.log" 2>&1 & echo $! > "$DATA/main.pid")
  wait_up "$PORT"
}

start_pair() {
  (cd "$REPO" || exit 1; HOME="$FIXTURE_HOME" SHEEPDOG_DATA_DIR="$DATA/pair" SHEEPDOG_STATE_PATH="$DATA/pair/state.json" \
    SHEEPDOG_WEB_BIND=0.0.0.0 SHEEPDOG_WEB_ADVERTISE="127.0.0.1:$PAIR_PORT" \
    nohup "$BIN" serve --config "$CONFIG" --brain=fake --herdr=mock \
      --voice=off --cli=off --dev --port "$PAIR_PORT" --env-file /dev/null \
      >>"$DATA/pair/daemon.log" 2>&1 & echo $! > "$DATA/pair.pid")
  wait_up "$PAIR_PORT"
}

# stop_main: the recorded PID, and a fixture daemon still listening on our
# own port (one an earlier run left behind).
stop_main() {
  local pid
  if [ -f "$DATA/main.pid" ]; then kill "$(cat "$DATA/main.pid")" 2>/dev/null || true; rm -f "$DATA/main.pid"; fi
  # Only a fixture daemon, never another program.
  pid=$(lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | head -n 1 || true)
  if [ -n "$pid" ] && ps -o command= -p "$pid" | grep -q "sheepdog serve .*--brain=fake .*--port $PORT"; then kill "$pid" 2>/dev/null || true; fi
  wait_down "$PORT"
}

stop_pair() {
  if [ -f "$DATA/pair.pid" ]; then kill "$(cat "$DATA/pair.pid")" 2>/dev/null || true; rm -f "$DATA/pair.pid"; fi
  wait_down "$PAIR_PORT"
}

# seed: one exchange, so the next start counts as a restart and the app
# draws "Sheepdog restarted" (HistoryApprovalUITests looks for it).
seed() {
  local i
  curl -fsS -o /dev/null --max-time 20 -H 'Content-Type: application/json' \
    -d '{"text":"what is going on"}' "http://127.0.0.1:$PORT/api/utterance"
  for i in $(seq 1 50); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/api/conversation" | grep -q 'what is going on'; then return 0; fi
    sleep 0.2
  done
  echo "fixture-daemons: the seeded utterance never reached /api/conversation" >&2
  return 1
}

case "${1:-}" in
  start)
    start_main
    seed
    stop_main
    start_main
    start_pair
    echo "fixture daemons up: 127.0.0.1:$PORT (main), 127.0.0.1:$PAIR_PORT (pair); data in $DATA"
    ;;
  restart)
    stop_main
    sleep "${SHEEPDOG_RESTART_GAP:-0}"
    start_main
    echo "fixture daemon restarted on 127.0.0.1:$PORT"
    ;;
  stop)
    stop_main
    stop_pair
    ;;
  *)
    echo "usage: $0 start|restart|stop" >&2
    exit 2
    ;;
esac
