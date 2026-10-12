#!/bin/bash
# usage: run-chunk.sh ARM PART KIND IDS REPEAT
# Tier 2, production config: brain DeepSeek deepseek-flash (Vertex registered as failover),
# voice Vertex Live gemini-3.8-live, Vertex TTS, chattiness chatty, simulated user on Vertex
# gemini-3.5-flash, two scratch daemons on :7911/:7912, judge fake (judged later).
set -u
ARM=$1; PART=$2; KIND=$3; IDS=$4; REP=$5
REPO=${REPO:?}; BIN=${BIN:?}; CFG=${CFG:?}
OUT=$REPO/docs/bench/results/2026-10-12-nc/$ARM/$PART
mkdir -p "$OUT"
cd $REPO
PERS=""
[ "$KIND" = workday ] && PERS="rotate"
$BIN bench run --tier 2 --env-file /Users/kc/Documents/herdr-sheepdog/.env \
  --config "$CFG" --scenarios "testdata/bench/scenarios/$KIND.json" --filter "$IDS" \
  --brain-provider deepseek --brain-model deepseek-flash \
  --simuser vertex --simuser-model gemini-3.5-flash --judge fake \
  --prompts-dir $REPO/docs/eval/prompt-arms/2026-10-11/$ARM \
  --ports 7911,7912 --parallel 2 --repeat $REP --personas "$PERS" \
  --budget-usd "${BUDGET:-6}" --out "$OUT" > "$OUT.stdout.log" 2>&1
echo "exit $?"; tail -3 "$OUT/spend.log" 2>/dev/null
