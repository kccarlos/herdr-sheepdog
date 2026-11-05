#!/usr/bin/env bash
# The scriptable part of the panel checklist (docs/dev/e2e/checklists/panel.md),
# on a private window: real key presses through System Events, results read
# from the daemon's API, hc and herdr-app.
#
#   scripts/e2e/panel-auto.sh [--shots DIR]
#
# Sends keys to the frontmost app, so run it only where nobody is typing: the
# nightly runs it inside the Tart VM (Accessibility is granted there). Needs
# macOS, Herdr.app and the Accessibility grant for the calling terminal/sshd.
# Checks: composer send (Enter, type, Enter: the line is in the conversation
# as typed, not in the terminal); Esc, Esc gives the keys back to the terminal
# (a typed command runs there); an approval shows at once and `1` on the
# panel answers it; daemon down and up leave the window without errors.
# Exits non-zero when a check failed; always tears down.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

SHOTS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --shots) SHOTS="${2:?--shots needs a directory}"; shift 2;;
    *) e2e_die "unknown argument: $1";;
  esac
done
[ "$(uname)" = Darwin ] || e2e_die "panel-auto needs macOS"

DIR=""
cleanup() {
  [ -n "$DIR" ] || return
  [ -n "$SHOTS" ] && { mkdir -p "$SHOTS"; cp "$DIR"/shots/*.png "$SHOTS/" 2>/dev/null; }
  "$HERE/down.sh" "$DIR" >/dev/null 2>&1
}
trap cleanup EXIT
trap 'exit 124' ALRM TERM INT

FAILS=0
ok() { echo "PASS  panel: $*"; }
bad() { echo "FAIL  panel: $*"; FAILS=$((FAILS + 1)); }
check() { # check <label> <cmd...>
  local label="$1"
  shift
  if "$@" >/dev/null 2>&1; then ok "$label"; else bad "$label"; fi
}

OUT=$("$HERE/up.sh") || { echo "$OUT"; echo "FAIL  panel: up.sh"; exit 1; }
DIR=$(echo "$OUT" | sed -n 's/^environment: //p')
# shellcheck disable=SC1091
. "$DIR/env.sh"
PID=$(cat "$DIR/app.pid")

se() { osascript -e "tell application \"System Events\" to $1" >/dev/null; }
raise() { se "set frontmost of (first process whose unix id is $PID) to true"; sleep 0.8; }
key() { se "key code $1"; sleep "${2:-0.5}"; }
typed() { se "keystroke \"$1\""; sleep 0.3; }
shot() { e2e_app shot "$DIR/shots/$1.png" >/dev/null 2>&1; }
terminal() { e2e_app ls | jq -r '[.spaces[].tabs[].terminals[].terminal] | first // empty'; }
screen() { e2e_hc read "$T"; }
convo_has() { e2e_api GET /api/conversation | jq -e --arg t "$1" '[.items[] | select(.kind=="shepherd" and .via=="typed" and .text==$t)] | length == 1'; }
no_errors() { e2e_app status | jq -e '.errors | length == 0'; }

T=$(terminal)
[ -n "$T" ] || { echo "FAIL  panel: no terminal in the private window"; exit 1; }
NONCE="nightly composer $RANDOM$RANDOM"

# Composer: the panel takes the keys (pane.go, as its key does), Enter opens
# the composer, Enter sends.
raise
e2e_app "do" pane.go --direction right >/dev/null
sleep 0.5
key 36
typed "$NONCE"
key 36 2
check "composer: Enter sends the typed line (in the conversation as typed)" e2e_wait 10 convo_has "$NONCE"
check "composer: the line did not go to the terminal" bash -c "! HC_SESSION='$E2E_HC_SESSION' HC_SOCKET='$E2E_HC_SOCKET' hc read '$T' | grep -qF '$NONCE'"
shot composer

# Esc in the composer goes to the panel, Esc on the panel gives the keys back
# to the terminal: a command typed now runs there (only the shell's output
# line is exactly esc-ok-42).
key 53 0.5
key 53 0.8
typed 'echo esc-ok-$((6*7))'
key 36 1
check "keys: Esc, Esc returns the keys to the terminal (typed command ran)" e2e_wait 10 bash -c "HC_SESSION='$E2E_HC_SESSION' HC_SOCKET='$E2E_HC_SOCKET' hc read '$T' | grep -qx 'esc-ok-42'"
shot esc-terminal

# Approvals show at once; inline cards answer on `1`, while in a short window
# (such as the VM's 662 px display) the compact "Answer" bar blocks blind `1`,
# opens on `a`, and answers on `1`.
"$HERE/scenario.sh" "$DIR" approval >/dev/null || bad "approval scenario"
check "approval: the question is open within 2 s" e2e_wait 2 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '[.questions[] | select(.kind==\"answer_prompt\")] | length == 1'"
sleep 1
shot approval
raise
e2e_app "do" pane.go --direction right >/dev/null
sleep 0.5
typed 1
WIN_H=$(jq -r '.bounds[3] // empty | floor' "$E2E_APP_HOME/window.json" 2>/dev/null)
[ -n "$WIN_H" ] || WIN_H=$(osascript -e "tell application \"System Events\" to item 2 of (get size of front window of (first process whose unix id is $PID))" 2>/dev/null || echo 0)
if [ "${WIN_H:-0}" -gt 0 ] && [ "$WIN_H" -lt 1074 ]; then
  sleep 0.5
  check "keys: 1 on a compact question bar does not blindly answer" bash -c "curl -fs '$E2E_URL/api/state' | jq -e '[.questions[] | select(.kind==\"answer_prompt\")] | length == 1'"
  typed a
  sleep 0.5
  typed 1
fi
sleep 1.5
check "keys: 1 on the panel answers the question (questions empty)" e2e_wait 10 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '(.questions | length == 0) and ([.agents[] | select(.alias==\"auth\") | .last_block.answered] | first == true)'"
shot approved
check "window: no mod errors after the answer" no_errors

# Daemon down and back: the window keeps running without errors.
"$HERE/scenario.sh" "$DIR" daemon-down >/dev/null || bad "daemon-down scenario"
sleep 3
shot daemon-down
check "daemon down: the window lists no errors" no_errors
"$HERE/scenario.sh" "$DIR" daemon-up >/dev/null || bad "daemon-up scenario"
sleep 5
shot daemon-up
check "daemon up: the window lists no errors" no_errors

if [ "$FAILS" -gt 0 ]; then echo "panel-auto: $FAILS FAILED"; exit 1; fi
echo "panel-auto: all passed"
