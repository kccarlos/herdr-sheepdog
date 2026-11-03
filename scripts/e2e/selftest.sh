#!/usr/bin/env bash
# Self-test of the e2e kit: up -> scenarios -> asserts -> down.
#   scripts/e2e/selftest.sh [--no-herdr-app]
# Exits non-zero on the first failed assertion; always tears down.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

UPARGS=()
APP=1
if [ "${1:-}" = "--no-herdr-app" ]; then UPARGS=(--no-herdr-app); APP=0; fi

DIR=""
cleanup() { [ -n "$DIR" ] && "$HERE/down.sh" "$DIR" >/dev/null 2>&1; }
trap cleanup EXIT

FAILS=0
ok() { echo "PASS  $*"; }
bad() { echo "FAIL  $*"; FAILS=$((FAILS + 1)); }
check() { # check <label> <cmd...>
  local label="$1"
  shift
  if "$@" >/dev/null 2>&1; then ok "$label"; else bad "$label"; fi
}
state() { curl -fsS --max-time 5 "$E2E_URL/api/state"; }
jq_state() { state | jq -e "$1"; }

OUT=$("$HERE/up.sh" ${UPARGS[@]+"${UPARGS[@]}"}) || { echo "$OUT"; echo "FAIL  up.sh"; exit 1; }
DIR=$(echo "$OUT" | sed -n 's/^environment: //p')
[ -n "$DIR" ] || { echo "$OUT"; echo "FAIL  no environment directory printed"; exit 1; }
# shellcheck disable=SC1091
. "$DIR/env.sh"
echo "up: $DIR on $E2E_URL"

check "daemon reports voice off, mock herdr" jq_state '.voice.mode == "off"'
if [ "$APP" = 1 ]; then
  check "herdr-app status: layout sheepdog, no errors, no overlay" bash -c "
    HERDR_APP_HOME='$E2E_APP_HOME' HC_SESSION='$E2E_HC_SESSION' HC_SOCKET='$E2E_HC_SOCKET' herdr-app status |
    jq -e '.layout == \"sheepdog\" and (.errors | length == 0) and (.overlays | length == 0)'"
fi

"$HERE/scenario.sh" "$DIR" approval >/dev/null
check "approval: a real answer_prompt is pending" jq_state '[.questions[] | select(.kind=="answer_prompt" and .agent=="auth")] | length == 1'
check "approval: the card offers a yes and a no key" jq_state '.questions[0] | (.yes_keys | length > 0) and (.no_keys | length > 0)'

"$HERE/scenario.sh" "$DIR" agent-blocked >/dev/null
check "agent-blocked: docs is blocked" jq_state '[.agents[] | select(.alias=="docs" and .state=="blocked")] | length == 1'
"$HERE/scenario.sh" "$DIR" agent-working >/dev/null
check "agent-working: build is working" jq_state '[.agents[] | select(.alias=="build" and .state=="working")] | length == 1'
"$HERE/scenario.sh" "$DIR" agent-done >/dev/null
check "agent-done: build is done" jq_state '[.agents[] | select(.alias=="build" and .state=="done")] | length == 1'

"$HERE/scenario.sh" "$DIR" voice-client connected >/dev/null
check "voice-client: a macos client holds the voice" jq_state '.voice.client_connected == true and .voice.client.platform == "macos"'
"$HERE/scenario.sh" "$DIR" voice-client disconnected >/dev/null
check "voice-client: disconnected" jq_state '.voice.client_connected == false'

"$HERE/scenario.sh" "$DIR" voice-on >/dev/null
check "voice-on: fake voice connected" jq_state '.voice.mode == "fake" and .voice.status == "connected"'
. "$DIR/env.sh"
"$HERE/scenario.sh" "$DIR" voice-client speaking >/dev/null
check "voice-client speaking: the voice speaks to the client" jq_state '.voice.speaking == true and .voice.client_connected == true'
"$HERE/scenario.sh" "$DIR" voice-client disconnected >/dev/null

"$HERE/scenario.sh" "$DIR" daemon-down >/dev/null
check "daemon-down: the port no longer answers" bash -c "! curl -fs --max-time 2 '$E2E_URL/api/state'"
if [ "$APP" = 1 ]; then
  sleep 2
  check "daemon-down: the window still answers (panel shows its own error)" bash -c "
    HERDR_APP_HOME='$E2E_APP_HOME' HC_SESSION='$E2E_HC_SESSION' HC_SOCKET='$E2E_HC_SOCKET' herdr-app status | jq -e '.errors | length == 0'"
fi
"$HERE/scenario.sh" "$DIR" daemon-up >/dev/null
check "daemon-up: answers again" jq_state '.voice.status != null'

if [ "$APP" = 1 ]; then
  "$HERE/shot.sh" "$DIR" selftest >/dev/null 2>&1
  check "shot: png written" test -s "$DIR/shots/selftest.png"
fi

DAEMON_PID=$(cat "$DIR/daemon.pid")
"$HERE/down.sh" "$DIR" >/dev/null
check "down: directory removed" test ! -e "$DIR"
check "down: daemon process gone" e2e_dead "$DAEMON_PID"
"$HERE/down.sh" "$DIR" >/dev/null
check "down: second run is a no-op" test ! -e "$DIR"
DIR=""

if [ "$FAILS" -gt 0 ]; then echo "selftest: $FAILS FAILED"; exit 1; fi
echo "selftest: all passed"
