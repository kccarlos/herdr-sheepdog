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

# e2e_daemon_start: start the private daemon and record its pid in
# <dir>/daemon.pid. Modes: E2E_VOICE (default off), E2E_BRAIN (fake),
# E2E_HERDR (mock; socket aims it at the private core through hc), E2E_PUSH_RELAY
# (the push stub's URL, when up.sh started one), E2E_CLI
# (on). E2E_WRAP, when set, is a command the daemon runs under (the nightly's
# real voice uses `op run ... --`, which puts the key into the daemon's
# environment only); the recorded pid is then the one listening on the port.
e2e_daemon_start() {
  local herdr_env=() wrap=()
  if [ "${E2E_HERDR:-mock}" = socket ]; then
    herdr_env=(SHEEPDOG_HERDR_BACKEND=hc "SHEEPDOG_HC_SOCKET=$E2E_HC_SOCKET")
  fi
  # up.sh --push-stub: push goes to a local stand-in for the relay, never to
  # the shared one.
  if [ -n "${E2E_PUSH_RELAY:-}" ]; then
    herdr_env+=(SHEEPDOG_PUSH_MODE=relay "SHEEPDOG_PUSH_RELAY_URL=$E2E_PUSH_RELAY")
  fi
  # shellcheck disable=SC2206 # E2E_WRAP is a word list on purpose
  [ -n "${E2E_WRAP:-}" ] && wrap=($E2E_WRAP)
  nohup ${wrap[@]+"${wrap[@]}"} env HOME="$E2E_DIR/home" XDG_CONFIG_HOME="$E2E_DIR/xdg/config" \
      XDG_DATA_HOME="$E2E_DIR/xdg/data" XDG_STATE_HOME="$E2E_DIR/xdg/state" \
      XDG_CACHE_HOME="$E2E_DIR/xdg/cache" ${herdr_env[@]+"${herdr_env[@]}"} \
      "$E2E_DIR/sheepdog" serve --brain="${E2E_BRAIN:-fake}" --herdr="${E2E_HERDR:-mock}" \
      --voice="${E2E_VOICE:-off}" --cli="${E2E_CLI:-on}" --dev \
      --port "$E2E_PORT" --data-dir "$E2E_DIR/data" </dev/null >"$E2E_DIR/daemon.log" 2>&1 &
  echo $! >"$E2E_DIR/daemon.pid"
  if ! e2e_wait "${E2E_START_WAIT:-30}" curl -fs --max-time 2 "$E2E_URL/api/state"; then
    e2e_kill "$(cat "$E2E_DIR/daemon.pid")"
    e2e_die "daemon did not come up; see $E2E_DIR/daemon.log"
  fi
  if [ -n "${E2E_WRAP:-}" ]; then
    # The wrapper's pid is $!; the daemon is its child, found by its port.
    local lp
    lp=$(lsof -t -nP -iTCP:"$E2E_PORT" -sTCP:LISTEN 2>/dev/null | head -1 || true)
    if [ -n "$lp" ]; then
      cat "$E2E_DIR/daemon.pid" >"$E2E_DIR/wrap.pid"
      echo "$lp" >"$E2E_DIR/daemon.pid"
    fi
  fi
  return 0
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
