#!/usr/bin/env bash
# Tear down a private e2e environment: scripts/e2e/down.sh <dir>
# Kills only the pids up.sh recorded, stops the private core, removes <dir>.
# Idempotent; never matches processes by name.
set -uo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

DIR="${1:-}"
e2e_check_dir "$DIR"
[ -d "$DIR" ] || { echo "already down: $DIR"; exit 0; }

if [ -f "$DIR/env.sh" ]; then
  # shellcheck disable=SC1091
  . "$DIR/env.sh"
  # Each pid file is trusted only while the pid still looks like ours.
  [ -f "$DIR/ws.pid" ] && e2e_kill "$(cat "$DIR/ws.pid")"
  [ -f "$DIR/relay.pid" ] && e2e_kill "$(cat "$DIR/relay.pid")"
  if [ -f "$DIR/app.pid" ]; then
    P=$(cat "$DIR/app.pid")
    if e2e_alive "$P" && ps -E -p "$P" 2>/dev/null | grep -q "HERDR_APP_HOME=$E2E_APP_HOME"; then
      e2e_kill "$P"
    fi
  fi
  [ -f "$DIR/daemon.pid" ] && e2e_kill "$(cat "$DIR/daemon.pid")"
  [ -f "$DIR/wrap.pid" ] && e2e_kill "$(cat "$DIR/wrap.pid")"
  if [ -S "$E2E_HC_SOCKET" ]; then
    e2e_hc server stop >/dev/null 2>&1 || true
  fi
  [ -f "$DIR/hc.pid" ] && {
    P=$(cat "$DIR/hc.pid")
    # Only if the core is still serving our private socket.
    if e2e_alive "$P" && lsof -p "$P" 2>/dev/null | grep -q "$E2E_HC_SOCKET"; then e2e_kill "$P"; fi
  }
fi
rm -rf "$DIR"
echo "down: $DIR"
