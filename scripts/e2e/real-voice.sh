#!/usr/bin/env bash
# Real-voice end-to-end check (docs/dev/e2e.md, "Nightly regression").
#
#   scripts/e2e/real-voice.sh [--only s1,s2] [--out DIR]
#
# Runs a private daemon with the real Gemini Live voice (--voice=gemini, the
# fake orchestrator, mock herdr), its key injected by `op run` into the
# daemon's environment only, and speaks audio-e2e scenarios into its /ws
# (`sheepdog audio-e2e`: macOS `say` + afconvert, real-time pace). audio-e2e
# checks each scenario's transcript, tool call and spoken reply in /events.
# Each scenario is one real voice turn, reserved from scripts/e2e/budget.sh
# first. Default: status-question (heard, relayed, answered aloud) and
# room-noise-only (cafe noise is not heard as speech); approval-yes needs the
# real orchestrator, so it is not here.
#
# The key: the 1Password service account token in
# ~/.config/op/cloud-agents.token (docs: the wiki's cloud secrets page) and
# the reference in SHEEPDOG_NIGHTLY_GEMINI_REF (default
# op://Developer/GEMINI_API_KEY/credential). No value is ever
# printed or written to disk.
# Exit: 0 pass, 1 fail, 3 skip (no op, no token, budget used up, not macOS).
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

ONLY="status-question,room-noise-only"
OUTDIR=""
while [ $# -gt 0 ]; do
  case "$1" in
    --only) ONLY="${2:?--only needs scenario names}"; shift 2;;
    --out) OUTDIR="${2:?--out needs a directory}"; shift 2;;
    *) e2e_die "unknown argument: $1";;
  esac
done

skip() { echo "SKIP  real-voice: $*"; exit 3; }
TOKEN_FILE="${SHEEPDOG_OP_TOKEN_FILE:-$HOME/.config/op/cloud-agents.token}"
REF="${SHEEPDOG_NIGHTLY_GEMINI_REF:-op://Developer/GEMINI_API_KEY/credential}"
[ "$(uname)" = Darwin ] || skip "needs macOS (say, afconvert)"
command -v op >/dev/null 2>&1 || skip "the 1Password CLI (op) is not installed"
[ -r "$TOKEN_FILE" ] || skip "no service account token at $TOKEN_FILE"
command -v say >/dev/null 2>&1 && command -v afconvert >/dev/null 2>&1 || skip "say/afconvert missing"

N=$(echo "$ONLY" | tr ',' '\n' | grep -c .)
"$HERE/budget.sh" reserve voice "$N" || skip "voice budget used up for today"

DIR=""
cleanup() {
  [ -n "$DIR" ] || return
  [ -n "$OUTDIR" ] && cp "$DIR/daemon.log" "$OUTDIR/daemon-real-voice.log" 2>/dev/null
  "$HERE/down.sh" "$DIR" >/dev/null 2>&1
}
trap cleanup EXIT
trap 'exit 124' ALRM TERM INT

OUT=$("$HERE/up.sh" --no-herdr-app 2>&1) || { echo "$OUT"; echo "FAIL  real-voice: up.sh"; exit 1; }
DIR=$(echo "$OUT" | sed -n 's/^environment: //p')
# shellcheck disable=SC1091
. "$DIR/env.sh"

# Restart the private daemon with the real voice. The template holds only the
# reference; op resolves it into the daemon's environment and drops its own
# token from what the daemon sees.
printf 'GEMINI_API_KEY=%s\n' "$REF" >"$DIR/voice.env.tpl"
"$HERE/scenario.sh" "$DIR" daemon-down >/dev/null || { echo "FAIL  real-voice: could not stop the mock daemon"; exit 1; }
sed "s/^export E2E_VOICE=.*/export E2E_VOICE=gemini/" "$DIR/env.sh" >"$DIR/env.sh.new" && mv "$DIR/env.sh.new" "$DIR/env.sh"
export E2E_VOICE=gemini
OP_SERVICE_ACCOUNT_TOKEN="$(<"$TOKEN_FILE")"
export OP_SERVICE_ACCOUNT_TOKEN
E2E_WRAP="op run --env-file=$DIR/voice.env.tpl -- env -u OP_SERVICE_ACCOUNT_TOKEN"
export E2E_WRAP
START_OK=1
(e2e_daemon_start) || START_OK=0
unset OP_SERVICE_ACCOUNT_TOKEN E2E_WRAP
[ "$START_OK" = 1 ] || { echo "FAIL  real-voice: the daemon did not start with the real voice (see daemon log)"; exit 1; }

echo "voice: $(curl -fs "$E2E_URL/api/state" | jq -c '.voice | {mode, status, provider}')"

ARGS=(audio-e2e --url "$E2E_URL" --only "$ONLY" --scenarios "$E2E_ROOT/testdata/audio-e2e/scenarios.json"
  --cache "${TMPDIR:-/tmp}/sheepdog-audio-e2e")
[ -n "$OUTDIR" ] && ARGS+=(--json "$OUTDIR/real-voice.json")
if "$DIR/sheepdog" "${ARGS[@]}"; then
  echo "PASS  real-voice: audio-e2e $ONLY (transcript, tool and reply checked in /events)"
  exit 0
fi
echo "FAIL  real-voice: audio-e2e $ONLY"
exit 1
