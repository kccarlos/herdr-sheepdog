#!/usr/bin/env bash
# Real-agent end-to-end check (docs/dev/e2e.md, "Nightly regression").
#
#   scripts/e2e/real-agent.sh claude|codex [--timeout S] [--budget-reserved]
#
# Starts a private environment whose daemon watches the private core
# (--herdr socket, --brain off, --cli off, no window), runs one real, cheap
# agent there (Claude Code on Haiku, or Codex on its configured model) in a
# throwaway git repo under /tmp with a one-line task that needs a permission
# prompt, and checks that sheepdog detects the agent, sees it blocked, raises
# an answer_prompt question, and that answering it through the API unblocks
# the agent (the file the task makes appears). A folder-trust question before
# the task is answered the same way.
#
# The agent runs with the real HOME (its own CLI login lives there; nothing is
# copied); the daemon, the core's state and the repo are private.
# One run is one agent task from scripts/e2e/budget.sh (--budget-reserved:
# the caller already took it, e.g. the nightly for a remote machine).
# Exit: 0 pass, 1 fail, 3 skip (CLI missing or not logged in, budget used up).
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

AGENT="${1:-}"
shift || true
LIMIT=300
RESERVED=0
while [ $# -gt 0 ]; do
  case "$1" in
    --timeout) LIMIT="${2:?--timeout needs seconds}"; shift 2;;
    --budget-reserved) RESERVED=1; shift;;
    *) e2e_die "unknown argument: $1";;
  esac
done

MARK=nightly-ok.txt
TASK="Run exactly this one shell command and nothing else: touch $MARK"
case "$AGENT" in
  claude)
    BIN=claude
    # manual: ask before every tool, whatever the user's default mode is.
    ARGV=(claude --model haiku --permission-mode manual "$TASK")
    ;;
  codex)
    BIN=codex
    # read-only sandbox + on-request: writing a file needs an approval.
    ARGV=(codex --sandbox read-only --ask-for-approval on-request "$TASK")
    [ -n "${NIGHTLY_CODEX_MODEL:-}" ] && ARGV=(codex -m "$NIGHTLY_CODEX_MODEL" --sandbox read-only --ask-for-approval on-request "$TASK")
    ;;
  *) e2e_die "usage: real-agent.sh claude|codex [--timeout S]";;
esac

skip() { echo "SKIP  real-agent $AGENT: $*"; exit 3; }
pass() { echo "PASS  real-agent $AGENT: $*"; }
fail() { echo "FAIL  real-agent $AGENT: $*"; FAILED=1; }

command -v "$BIN" >/dev/null 2>&1 || skip "$BIN is not installed"
case "$AGENT" in
  claude) "$BIN" auth status >/dev/null 2>&1 || skip "claude is not logged in (claude auth status)";;
  codex) "$BIN" login status >/dev/null 2>&1 || skip "codex is not logged in (codex login status)";;
esac
if [ "$RESERVED" = 0 ]; then
  "$HERE/budget.sh" reserve agent 1 || skip "agent budget used up for today"
fi

# A fixed path, so the CLI's folder trust is remembered from night to night.
REPO="/tmp/sheepdog-nightly-agent/$AGENT"
rm -rf "$REPO"
mkdir -p "$REPO"
(cd "$REPO" && git init -q && printf '# throwaway\n' >README.md && git add README.md &&
  git -c user.name=nightly -c user.email=nightly@localhost commit -qm init) || e2e_die "could not make $REPO"

DIR=""
FAILED=0
cleanup() {
  [ -n "$DIR" ] || return
  [ -n "${TERM_ID:-}" ] && e2e_hc kill "$TERM_ID" >/dev/null 2>&1
  if [ -n "${KEEP_LOG:-}" ]; then
    cp "$DIR/daemon.log" "$KEEP_LOG/daemon-real-agent-$AGENT.log" 2>/dev/null
    e2e_hc read "$TERM_ID" >"$KEEP_LOG/screen-real-agent-$AGENT.txt" 2>/dev/null
  fi
  "$HERE/down.sh" "$DIR" >/dev/null 2>&1
}
trap cleanup EXIT
trap 'exit 124' ALRM TERM INT

OUT=$("$HERE/up.sh" --no-herdr-app --herdr socket --brain off --cli off 2>&1) || { echo "$OUT"; fail "up.sh"; exit 1; }
DIR=$(echo "$OUT" | sed -n 's/^environment: //p')
# shellcheck disable=SC1091
. "$DIR/env.sh"
KEEP_LOG="${NIGHTLY_OUT:-}"

state() { curl -fsS --max-time 5 "$E2E_URL/api/state"; }

RUN=$(cd "$REPO" && e2e_hc run --json --bg --no-parent --name "nightly-$AGENT" --cwd "$REPO" -- "${ARGV[@]}" 2>&1) || { echo "$RUN"; fail "hc run"; exit 1; }
TERM_ID=$(echo "$RUN" | jq -r '.address // .terminal // empty' 2>/dev/null)
[ -n "$TERM_ID" ] || TERM_ID="nightly-$AGENT"
echo "started $AGENT in $TERM_ID ($REPO)"

# A CLI that says it is logged in can still fail its first call (a revoked
# token): that is a SKIP with the CLI's words, not a sheepdog failure.
AUTH_ERR='sign in again|refresh token|not logged in|please log ?in|run /login|invalid api key|authentication_error|OAuth token has expired'
auth_broken() { e2e_hc read "$TERM_ID" 2>/dev/null | grep -iE "$AUTH_ERR" | head -1; }

DETECTED=0 BLOCKED=0 ASKED=0 ANSWERED=0 TICK=0
DEADLINE=$(( $(date +%s) + LIMIT ))
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  TICK=$((TICK + 1))
  if [ "$ASKED" = 0 ] && [ $((TICK % 5)) = 0 ]; then
    WHY=$(auth_broken)
    [ -n "$WHY" ] && skip "the CLI cannot use its login: $(echo "$WHY" | tr -s ' ' | cut -c1-200)"
  fi
  S=$(state 2>/dev/null) || { sleep 1; continue; }
  ROW=$(echo "$S" | jq -c --arg k "$AGENT" '[.agents[]? | select(.kind==$k)] | first // empty')
  if [ -n "$ROW" ] && [ "$DETECTED" = 0 ]; then DETECTED=1; pass "detected as an agent ($(echo "$ROW" | jq -r '.alias'))"; fi
  if [ -n "$ROW" ] && [ "$(echo "$ROW" | jq -r '.state')" = blocked ] && [ "$BLOCKED" = 0 ]; then BLOCKED=1; pass "seen blocked"; fi
  Q=$(echo "$S" | jq -c '[.questions[]? | select(.kind=="answer_prompt")] | first // empty')
  if [ -n "$Q" ]; then
    ASKED=$((ASKED + 1))
    QID=$(echo "$Q" | jq -r '.id')
    OPT=$(echo "$Q" | jq -r '[.options[]? | select((.label // .text // "") | test("^(yes|allow|approve|proceed|trust)"; "i"))] | first | .index // 1')
    echo "question $QID: $(echo "$Q" | jq -c '{summary, options: [.options[]? | (.label // .text)]}' | cut -c1-300); answering $OPT"
    if curl -fsS --max-time 20 -X POST -H 'Content-Type: application/json' \
        -d "$(jq -nc --arg id "$QID" --argjson o "$OPT" '{id:$id, option:$o}')" "$E2E_URL/api/pending/answer" >/dev/null; then
      ANSWERED=$((ANSWERED + 1))
    fi
    sleep 2
    continue
  fi
  if [ -e "$REPO/$MARK" ]; then break; fi
  sleep 1
done

if [ "$ASKED" = 0 ]; then
  WHY=$(auth_broken)
  [ -n "$WHY" ] && skip "the CLI cannot use its login: $(echo "$WHY" | tr -s ' ' | cut -c1-200)"
fi
[ "$DETECTED" = 1 ] || fail "never detected as an agent"
[ "$BLOCKED" = 1 ] || fail "never seen blocked"
if [ "$ASKED" -ge 1 ] && [ "$ANSWERED" -ge 1 ]; then pass "raised $ASKED answer_prompt question(s), answered $ANSWERED through /api/pending/answer"; else fail "no answer_prompt raised and answered (asked $ASKED, answered $ANSWERED)"; fi
if [ -e "$REPO/$MARK" ]; then
  pass "unblocked: the task ran ($MARK exists)"
else
  fail "the task never ran ($MARK missing after ${LIMIT}s)"
  echo "--- the agent's screen now (last 20 lines) ---"
  e2e_hc read "$TERM_ID" 2>/dev/null | grep -v '^[[:space:]]*$' | tail -20
  echo "--- the daemon's answers (keys sent) ---"
  cat "$E2E_DIR"/data/events/*.jsonl 2>/dev/null | jq -c 'select(.type=="pending.confirmed" or .type=="herdr.send_keys") | {type, keys: .payload.keys, answer: .payload.answer}' 2>/dev/null | tail -10
fi
# Not blocked any more once answered.
if e2e_wait 30 bash -c "curl -fsS --max-time 5 '$E2E_URL/api/state' | jq -e --arg k '$AGENT' '[.agents[]? | select(.kind==\$k and .state==\"blocked\")] | length == 0'"; then
  pass "no longer blocked"
else
  fail "still blocked after the answer"
fi
exit "$FAILED"
