#!/usr/bin/env bash
# Put the private environment into a situation: scripts/e2e/scenario.sh <dir> <name> [arg]
#
#   approval              a real answer_prompt pending (question card + approval item)
#   agent-blocked         an agent blocked on a free-form question (no approval card)
#   agent-working         an agent that is working
#   agent-done            that agent finishing (created working first when missing)
#   voice-client [mode]   a /ws client holds the voice: connected (default) | speaking | disconnected
#   voice-on              restart the daemon with --voice=fake (voice open, no network)
#   voice-off             restart the daemon with --voice=off
#   daemon-down           stop the private daemon
#   daemon-up             start it again (same port, same voice mode)
#
# Nothing here leaves the private environment; see docs/dev/e2e.md.
set -euo pipefail
# shellcheck source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

e2e_load "${1:-}"
NAME="${2:?usage: scenario.sh <dir> <name> [arg]}"
ARG="${3:-}"

post() { e2e_api POST "$1" "$2" >/dev/null; }

daemon_stop() {
  [ -f "$E2E_DIR/daemon.pid" ] && e2e_kill "$(cat "$E2E_DIR/daemon.pid")"
  [ -f "$E2E_DIR/wrap.pid" ] && e2e_kill "$(cat "$E2E_DIR/wrap.pid")"
  rm -f "$E2E_DIR/daemon.pid" "$E2E_DIR/wrap.pid"
  e2e_wait 10 bash -c "! curl -fs --max-time 1 '$E2E_URL/api/state'" || e2e_die "daemon still answers on $E2E_URL"
}

set_voice() {
  E2E_VOICE="$1"
  # Portable in-place edit (BSD sed on macOS and GNU sed on Linux differ on -i).
  sed "s/^export E2E_VOICE=.*/export E2E_VOICE=$1/" "$E2E_DIR/env.sh" >"$E2E_DIR/env.sh.new" && mv "$E2E_DIR/env.sh.new" "$E2E_DIR/env.sh"
}

ws_stop() {
  [ -f "$E2E_DIR/ws.pid" ] && e2e_kill "$(cat "$E2E_DIR/ws.pid")"
  rm -f "$E2E_DIR/ws.pid"
}

voice_client_attached() { e2e_api GET /api/state | jq -e '.voice.client_connected == true' >/dev/null; }

case "$NAME" in
  approval)
    SCREEN="$E2E_ROOT/testdata/herdr/codex-approval-prompt.screen.txt"
    [ -f "$SCREEN" ] || e2e_die "missing $SCREEN"
    post /api/inject/blocked "$(jq -n --rawfile s "$SCREEN" '{pane_id:"e2e:approval",alias:"auth",kind:"codex",screen:$s}')"
    e2e_api GET /api/state | jq -e '[.questions[] | select(.kind=="answer_prompt")] | length >= 1' >/dev/null \
      || e2e_die "no answer_prompt pending after the inject"
    echo "approval: answer_prompt pending for pane e2e:approval (agent auth)"
    ;;
  agent-blocked)
    post /api/inject/pane "$(jq -n '{pane_id:"e2e:blocked",alias:"docs",kind:"claude",state:"blocked",screen:"I can document this two ways.\nWhich one should I use, the short README or the full guide?\n> "}')"
    echo "agent-blocked: agent docs is blocked on a question"
    ;;
  agent-working)
    post /api/inject/pane "$(jq -n '{pane_id:"e2e:work",alias:"build",kind:"codex",state:"working",screen:"Running the test suite...\n"}')"
    echo "agent-working: agent build is working"
    ;;
  agent-done)
    e2e_api GET /api/agents | jq -e '[.. | objects | select(.pane_id? == "e2e:work")] | length > 0' >/dev/null 2>&1 \
      || post /api/inject/pane "$(jq -n '{pane_id:"e2e:work",alias:"build",kind:"codex",state:"working",screen:"Running the test suite...\n"}')"
    post /api/inject/state "$(jq -n '{pane_id:"e2e:work",state:"done",screen:"All 42 tests pass. Done.\n"}')"
    echo "agent-done: agent build finished"
    ;;
  voice-on)
    daemon_stop
    set_voice fake
    e2e_daemon_start
    e2e_wait 15 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '.voice.status == \"connected\"'" || e2e_die "fake voice never reported connected"
    echo "voice-on: daemon restarted with --voice=fake; voice connected"
    ;;
  voice-off)
    ws_stop
    daemon_stop
    set_voice off
    e2e_daemon_start
    echo "voice-off: daemon restarted with --voice=off"
    ;;
  voice-client)
    MODE="${ARG:-connected}"
    case "$MODE" in
      disconnected)
        ws_stop
        e2e_wait 10 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '.voice.client_connected == false'" || e2e_die "client still attached"
        echo "voice-client: disconnected"
        ;;
      connected|speaking)
        if [ "$MODE" = speaking ] && [ "${E2E_VOICE:-off}" != fake ]; then
          e2e_die "speaking needs a voice: run 'scenario.sh $E2E_DIR voice-on' first"
        fi
        ws_stop
        BIN="$E2E_DIR/wsclient"
        [ -x "$BIN" ] || (cd "$E2E_ROOT" && go build -o "$BIN" ./scripts/e2e/wsclient)
        SAY=()
        [ "$MODE" = speaking ] && SAY=(-say "e2e speaking check")
        nohup "$BIN" -url "ws://127.0.0.1:$E2E_PORT/ws" -max 20m ${SAY[@]+"${SAY[@]}"} >"$E2E_DIR/ws.log" 2>&1 &
        echo $! >"$E2E_DIR/ws.pid"
        e2e_wait 10 voice_client_attached || e2e_die "client never attached; see $E2E_DIR/ws.log"
        if [ "$MODE" = speaking ]; then
          # The fake voice speaks for 6 s after the text message.
          e2e_wait 6 bash -c "curl -fs '$E2E_URL/api/state' | jq -e '.voice.speaking == true'" || e2e_die "voice never reported speaking"
        fi
        echo "voice-client: $MODE (macos client, pid $(cat "$E2E_DIR/ws.pid"))"
        ;;
      *) e2e_die "voice-client mode: connected | speaking | disconnected";;
    esac
    ;;
  daemon-down)
    ws_stop
    daemon_stop
    echo "daemon-down: $E2E_URL no longer answers"
    ;;
  daemon-up)
    if curl -fs --max-time 1 "$E2E_URL/api/state" >/dev/null 2>&1; then echo "daemon-up: already up"; exit 0; fi
    e2e_daemon_start
    echo "daemon-up: $E2E_URL answers again"
    ;;
  *) e2e_die "unknown scenario: $NAME";;
esac
