#!/usr/bin/env bash
# The daily budget of real (paid) checks (docs/dev/e2e.md, "Nightly regression").
#
#   scripts/e2e/budget.sh reserve voice|agent N   take N, or refuse (exit 1)
#   scripts/e2e/budget.sh show                    print today's counts
#
# Counts live in $SHEEPDOG_BUDGET_FILE (default
# ~/Library/Logs/sheepdog-nightly/budget.json) as {"date", "voice", "agent"};
# a new local day starts from zero. Caps per day: SHEEPDOG_BUDGET_VOICE (20
# real voice turns) and SHEEPDOG_BUDGET_AGENT (30 small agent tasks). A turn
# is reserved before it runs, so a failed run still counts.
set -euo pipefail

FILE="${SHEEPDOG_BUDGET_FILE:-$HOME/Library/Logs/sheepdog-nightly/budget.json}"
CAP_VOICE="${SHEEPDOG_BUDGET_VOICE:-20}"
CAP_AGENT="${SHEEPDOG_BUDGET_AGENT:-30}"
TODAY=$(date +%F)
mkdir -p "$(dirname "$FILE")"

current() {
  if [ -s "$FILE" ] && jq -e --arg d "$TODAY" '.date == $d' "$FILE" >/dev/null 2>&1; then
    cat "$FILE"
  else
    jq -n --arg d "$TODAY" '{date:$d, voice:0, agent:0}'
  fi
}

case "${1:-}" in
  show)
    current | jq -c --argjson cv "$CAP_VOICE" --argjson ca "$CAP_AGENT" '. + {voice_cap:$cv, agent_cap:$ca}'
    ;;
  reserve)
    KIND="${2:-}"
    N="${3:-1}"
    case "$KIND" in
      voice) CAP=$CAP_VOICE;;
      agent) CAP=$CAP_AGENT;;
      *) echo "budget: kind is voice or agent" >&2; exit 2;;
    esac
    LOCK="$FILE.lock"
    for _ in $(seq 1 50); do mkdir "$LOCK" 2>/dev/null && break; sleep 0.2; done
    [ -d "$LOCK" ] || { echo "budget: $LOCK is held" >&2; exit 1; }
    trap 'rmdir "$LOCK" 2>/dev/null' EXIT
    CUR=$(current)
    USED=$(echo "$CUR" | jq -r --arg k "$KIND" '.[$k] // 0')
    if [ $((USED + N)) -gt "$CAP" ]; then
      echo "budget: $KIND $USED/$CAP used today; $N more would exceed the cap" >&2
      exit 1
    fi
    echo "$CUR" | jq --arg k "$KIND" --argjson n "$N" '.[$k] = ((.[$k] // 0) + $n)' >"$FILE.new"
    mv "$FILE.new" "$FILE"
    echo "budget: $KIND $((USED + N))/$CAP today"
    ;;
  *) sed -n '2,11p' "$0"; exit 2;;
esac
