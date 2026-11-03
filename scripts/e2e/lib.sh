# Shared helpers for scripts/e2e. Sourced, not run.
# Everything an environment owns lives in one directory (<dir>/env.sh records
# it); no script here ever touches the live daemon, ~/.config/sheepdog,
# ~/.local/share/sheepdog, ~/.config/herdr-app or the default hc core.

E2E_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

e2e_die() { echo "e2e: $*" >&2; exit 1; }

# e2e_check_dir <dir>: refuse anything that is not an e2e directory.
e2e_check_dir() {
  [ -n "${1:-}" ] || e2e_die "usage: $0 <dir> ..."
  case "$1" in /tmp/sde-*) ;; *) e2e_die "not an e2e dir: $1";; esac
}

# e2e_load <dir>: source <dir>/env.sh.
e2e_load() {
  e2e_check_dir "${1:-}"
  [ -f "$1/env.sh" ] || e2e_die "no $1/env.sh (already down?)"
  # shellcheck disable=SC1091
  . "$1/env.sh"
}

# e2e_wait <seconds> <cmd...>: poll cmd every 0.25 s until it succeeds or the
# time is up (returns 1).
e2e_wait() {
  local n=$(( $1 * 4 ))
  shift
  while [ "$n" -gt 0 ]; do
    "$@" >/dev/null 2>&1 && return 0
    sleep 0.25
    n=$((n - 1))
  done
  return 1
}

e2e_alive() { [ -n "${1:-}" ] && kill -0 "$1" 2>/dev/null; }
e2e_dead() { ! e2e_alive "${1:-}"; }

# e2e_kill <pid>: TERM, wait up to 5 s, then KILL. Only ever a recorded pid.
e2e_kill() {
  e2e_alive "${1:-}" || return 0
  kill "$1" 2>/dev/null
  e2e_wait 5 e2e_dead "$1" || kill -9 "$1" 2>/dev/null
  return 0
}

# e2e_api <method> <path> [json]: call the private daemon.
e2e_api() {
  if [ -n "${3:-}" ]; then
    curl -fsS --max-time 20 -X "$1" -H 'Content-Type: application/json' -d "$3" "$E2E_URL$2"
  else
    curl -fsS --max-time 20 -X "$1" "$E2E_URL$2"
  fi
}

# e2e_daemon_start: start the private daemon (voice mode E2E_VOICE, default
# off) and record its pid in <dir>/daemon.pid.
e2e_daemon_start() {
  env HOME="$E2E_DIR/home" XDG_CONFIG_HOME="$E2E_DIR/xdg/config" \
      XDG_DATA_HOME="$E2E_DIR/xdg/data" XDG_STATE_HOME="$E2E_DIR/xdg/state" \
      XDG_CACHE_HOME="$E2E_DIR/xdg/cache" \
      nohup "$E2E_DIR/sheepdog" serve --brain=fake --herdr=mock --voice="${E2E_VOICE:-off}" --dev \
      --port "$E2E_PORT" --data-dir "$E2E_DIR/data" >"$E2E_DIR/daemon.log" 2>&1 &
  echo $! >"$E2E_DIR/daemon.pid"
  e2e_wait 30 curl -fs --max-time 2 "$E2E_URL/api/state" || e2e_die "daemon did not come up; see $E2E_DIR/daemon.log"
}

# e2e_app <herdr-app args...>: the herdr-app CLI aimed at the private window.
e2e_app() {
  env HERDR_APP_HOME="$E2E_APP_HOME" HC_SESSION="$E2E_HC_SESSION" HC_SOCKET="$E2E_HC_SOCKET" \
      XDG_CONFIG_HOME="$E2E_DIR/xdg/config" XDG_DATA_HOME="$E2E_DIR/xdg/data" \
      XDG_STATE_HOME="$E2E_DIR/xdg/state" XDG_CACHE_HOME="$E2E_DIR/xdg/cache" \
      herdr-app "$@"
}

e2e_hc() {
  env HC_SESSION="$E2E_HC_SESSION" HC_SOCKET="$E2E_HC_SOCKET" \
      XDG_CONFIG_HOME="$E2E_DIR/xdg/config" XDG_DATA_HOME="$E2E_DIR/xdg/data" \
      XDG_STATE_HOME="$E2E_DIR/xdg/state" XDG_CACHE_HOME="$E2E_DIR/xdg/cache" \
      hc "$@"
}
