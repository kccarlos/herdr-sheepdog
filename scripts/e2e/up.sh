#!/usr/bin/env bash
# One-command private end-to-end environment (docs/dev/e2e.md).
#
#   scripts/e2e/up.sh [--port N] [--panel-from <dir>] [--no-herdr-app]
#                     [--herdr mock|socket|hc] [--brain fake|off] [--cli on|off]
#                     [--push-stub]
#
# Builds the daemon from this worktree and runs it privately (fake
# orchestrator, mock herdr, no voice, dev routes; --herdr socket, or hc, points
# it at the private core instead; --push-stub starts a local stand-in for the
# push relay and aims the daemon's push at it) beside a private herdr-core
# and, unless --no-herdr-app, a private Herdr window showing the panel from
# --panel-from (a worktree root or a mods/sheepdog folder; default: this
# worktree). Prints the environment directory; `down.sh <dir>` removes it.
set -euo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PORT=""
PANEL_FROM="$E2E_ROOT"
WITH_APP=1
HERDR_MODE=mock
BRAIN_MODE=fake
CLI_MODE=on
PUSH_STUB=0
while [ $# -gt 0 ]; do
  case "$1" in
    --port) PORT="${2:?--port needs a number}"; shift 2;;
    --panel-from) PANEL_FROM="${2:?--panel-from needs a directory}"; shift 2;;
    --no-herdr-app) WITH_APP=0; shift;;
    --herdr)
      HERDR_MODE="${2:?--herdr needs mock, socket or hc}"
      # hc is the design's name for socket: the daemon on the private core.
      [ "$HERDR_MODE" = hc ] && HERDR_MODE=socket
      case "$HERDR_MODE" in mock|socket) ;; *) e2e_die "--herdr needs mock, socket or hc";; esac
      shift 2;;
    --push-stub) PUSH_STUB=1; shift;;
    --brain) BRAIN_MODE="${2:?--brain needs fake or off}"; shift 2;;
    --cli) CLI_MODE="${2:?--cli needs on or off}"; shift 2;;
    -h|--help) sed -n '2,15p' "$0"; exit 0;;
    *) e2e_die "unknown argument: $1";;
  esac
done

FREE_KB=$(df -k /tmp | awk 'NR==2{print $4}')
[ "$FREE_KB" -gt 8388608 ] || e2e_die "less than 8 GB free; refusing to build"

# The panel source: a mods/sheepdog folder, and the layout beside it.
if [ -f "$PANEL_FROM/main.js" ] && [ -f "$PANEL_FROM/net/config.js" ]; then
  PANEL_MOD="$PANEL_FROM"
  PANEL_LAYOUT="$(cd "$PANEL_FROM/../.." && pwd)/sheepdog-layout/layouts/sheepdog.json"
else
  PANEL_MOD="$PANEL_FROM/mods/sheepdog"
  PANEL_LAYOUT="$PANEL_FROM/sheepdog-layout/layouts/sheepdog.json"
fi
[ -f "$PANEL_MOD/net/config.js" ] || e2e_die "no panel at $PANEL_MOD"
[ -f "$PANEL_LAYOUT" ] || e2e_die "no layout at $PANEL_LAYOUT"

ID=""
for _ in 1 2 3 4 5 6 7 8; do
  ID=$(printf '%04x' $((RANDOM % 65536)))
  [ ! -e "/tmp/sde-$ID" ] && break
done
DIR="/tmp/sde-$ID"
mkdir -p "$DIR"/{home,data,shots,xdg/config,xdg/data,xdg/state,xdg/cache}

if [ -z "$PORT" ]; then
  # A free loopback port from 18000 up, never 7777.
  for p in $(seq 18000 18200); do
    lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1 || { PORT=$p; break; }
  done
fi
[ -n "$PORT" ] && [ "$PORT" != 7777 ] || e2e_die "no free port (7777 is never used)"
lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1 && e2e_die "port $PORT is in use"

HC_SESSION="sde$ID"
# A unix socket path is limited to ~104 bytes; /tmp/sde-xxxx/hc.sock is short.
HC_SOCKET="$DIR/hc.sock"
APP_HOME="$DIR/app-home"

echo "building the daemon from $E2E_ROOT ..."
(cd "$E2E_ROOT" && GOFLAGS=-trimpath go build -o "$DIR/sheepdog" ./cmd/sheepdog)

# The push relay stand-in: a loopback port, and a file of what it was sent.
PUSH_RELAY=""
if [ "$PUSH_STUB" = 1 ]; then
  RP=""
  for p in $(seq 18300 18400); do
    lsof -nP -iTCP:"$p" -sTCP:LISTEN >/dev/null 2>&1 || { RP=$p; break; }
  done
  [ -n "$RP" ] || e2e_die "no free port for the push stub"
  (cd "$E2E_ROOT" && GOFLAGS=-trimpath go build -o "$DIR/relaystub" ./scripts/e2e/relaystub)
  : >"$DIR/relay.jsonl"
  nohup "$DIR/relaystub" -addr "127.0.0.1:$RP" -log "$DIR/relay.jsonl" </dev/null >"$DIR/relay.log" 2>&1 &
  echo $! >"$DIR/relay.pid"
  PUSH_RELAY="http://127.0.0.1:$RP"
  e2e_wait 10 curl -fs --max-time 2 "$PUSH_RELAY/v1/health" || e2e_die "push stub did not come up; see $DIR/relay.log"
  echo "push stub up on $PUSH_RELAY (pid $(cat "$DIR/relay.pid")), log $DIR/relay.jsonl"
fi

cat >"$DIR/env.sh" <<ENV
# Written by scripts/e2e/up.sh. Source it to use the private environment.
export E2E_DIR=$DIR
export E2E_ID=$ID
export E2E_PORT=$PORT
export E2E_URL=http://127.0.0.1:$PORT
export E2E_HC_SESSION=$HC_SESSION
export E2E_HC_SOCKET=$HC_SOCKET
export E2E_APP_HOME=$APP_HOME
export E2E_WITH_APP=$WITH_APP
export E2E_VOICE=off
export E2E_HERDR=$HERDR_MODE
export E2E_BRAIN=$BRAIN_MODE
export E2E_CLI=$CLI_MODE
export E2E_PUSH_RELAY=$PUSH_RELAY
export E2E_PUSH_LOG=$DIR/relay.jsonl
export HERDR_APP_HOME=$APP_HOME
export HC_SESSION=$HC_SESSION
export HC_SOCKET=$HC_SOCKET
ENV
# shellcheck disable=SC1091
. "$DIR/env.sh"

# The private herdr-core.
e2e_hc server start >"$DIR/hc.log" 2>&1 || { cat "$DIR/hc.log" >&2; e2e_die "hc server start failed"; }
e2e_wait 15 test -S "$HC_SOCKET" || e2e_die "private core socket $HC_SOCKET never appeared"
HC_PID=$(lsof -t "$HC_SOCKET" 2>/dev/null | head -1 || true)
[ -n "$HC_PID" ] && echo "$HC_PID" >"$DIR/hc.pid"
echo "private core up (session $HC_SESSION, pid ${HC_PID:-unknown})"

e2e_daemon_start
echo "daemon up on $E2E_URL (pid $(cat "$DIR/daemon.pid"))"

if [ "$WITH_APP" = 1 ]; then
  mkdir -p "$APP_HOME/mods/sheepdog" "$APP_HOME/layouts"
  # The panel as shipped, unchanged (so the daemon does not see it as
  # modified); it finds the private daemon through the shared key
  # sheepdog.url written below, not by editing net/config.js.
  cp -R "$PANEL_MOD/." "$APP_HOME/mods/sheepdog/"
  cp "$PANEL_LAYOUT" "$APP_HOME/layouts/sheepdog.json"
  # No welcome dialog (no command closes it): write the key it sets when
  # finished, and the layout, before the first start.
  # Likewise the what's-new dialog an update opens once by itself.
  AV=$(herdr-app --version 2>/dev/null | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)
  [ -n "$AV" ] && printf '{"last_run":"%s","presented":["%s"]}\n' "$AV" "$AV" >"$APP_HOME/update-notes.json"
  printf '{"frame.welcomed":"skipped","layout":"sheepdog","sheepdog.url":"%s"}\n' "$E2E_URL" >"$APP_HOME/shared.json"

  APP="$HOME/Applications/Herdr.app"
  [ -d "$APP" ] || e2e_die "$APP not found"
  # open(1), not the herdr-app CLI symlink: a launch through the CLI is
  # hidden from computer-use screenshots.
  open -n -a "$APP" --env "HERDR_APP_HOME=$APP_HOME" --env "HC_SESSION=$HC_SESSION" \
    --env "HC_SOCKET=$HC_SOCKET" --env "XDG_CONFIG_HOME=$DIR/xdg/config" \
    --env "XDG_DATA_HOME=$DIR/xdg/data" --env "XDG_STATE_HOME=$DIR/xdg/state" \
    --env "XDG_CACHE_HOME=$DIR/xdg/cache" --env "DISABLE_AUTOUPDATER=1"

  # Find the private Herdr: the Herdr process whose environment has our
  # HERDR_APP_HOME (ps -E prints it).
  find_app_pid() {
    ps -axo pid=,command= | awk '/Herdr.app\/Contents\/MacOS\/Herdr/ && !/awk/ {print $1}' | while read -r p; do
      if ps -E -p "$p" 2>/dev/null | grep -q "HERDR_APP_HOME=$APP_HOME"; then echo "$p"; break; fi
    done
  }
  APP_PID=""
  for _ in $(seq 1 60); do
    APP_PID=$(find_app_pid)
    [ -n "$APP_PID" ] && break
    sleep 0.5
  done
  [ -n "$APP_PID" ] || e2e_die "private Herdr did not start"
  echo "$APP_PID" >"$DIR/app.pid"
  e2e_wait 30 e2e_app status || e2e_die "private Herdr window never answered 'herdr-app status'"

  e2e_app do layout.switch --name sheepdog >/dev/null || echo "warning: layout.switch failed" >&2
  e2e_app do space.new --name e2e --folder "$DIR/home" >/dev/null || echo "warning: space.new failed" >&2
  e2e_app do window.maximize >/dev/null 2>&1 || true
  sleep 1
  echo "private Herdr up (pid $APP_PID)"
fi

cat <<OUT

environment: $DIR
  source $DIR/env.sh      # E2E_URL, HERDR_APP_HOME, HC_SESSION, HC_SOCKET
  scripts/e2e/scenario.sh $DIR <approval|voice-client|voice-on|agent-blocked|agent-working|agent-done|agent-unread|agent-seen|agent-pin|space-mute|label-sync|daemon-down|daemon-up>
  scripts/e2e/shot.sh $DIR <name>
  scripts/e2e/down.sh $DIR
OUT
if [ "$WITH_APP" = 1 ]; then
  cat <<OUT
raise the private window (does not touch yours):
  osascript -e 'tell application "System Events" to set frontmost of (first process whose unix id is $(cat "$DIR/app.pid")) to true'
OUT
fi
