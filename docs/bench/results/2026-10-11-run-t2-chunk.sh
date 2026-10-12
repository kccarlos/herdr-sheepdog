#!/bin/bash
# usage: run-t2-chunk.sh ARM PART KIND IDS REPEAT
#   ARM a|rl|nc; KIND explain|workday; IDS comma list of scenario ids
# Tier 2 on Vertex (TTS, Gemini Live gemini-3.8-live, brain gemini-3.5-flash, DeepSeek failover),
# two scratch daemons, simulated user on DeepSeek, judge fake (judged later).
set -u
ARM=$1; PART=$2; KIND=$3; IDS=$4; REP=$5
REPO=/Users/kc/Documents/sheepdog-vx
OUT=$REPO/docs/bench/results/2026-10-11-t2/$ARM/$PART
mkdir -p "$OUT"
cd $REPO
PERS=""
[ "$KIND" = workday ] && PERS="rotate"
/tmp/sheepdog-vx bench run --tier 2 --env-file /Users/kc/Documents/herdr-sheepdog/.env \
  --config "$CFG" --scenarios "testdata/bench/scenarios/$KIND.json" --filter "$IDS" \
  --brain-provider vertex --brain-model gemini-3.5-flash \
  --simuser deepseek --simuser-model deepseek-flash --judge fake \
  --prompts-dir $REPO/docs/eval/prompt-arms/2026-10-11/$ARM \
  --ports 7901,7902 --parallel 2 --repeat $REP --personas "$PERS" \
  --budget-usd "${BUDGET:-4}" --out "$OUT" > "$OUT.stdout.log" 2>&1
echo "exit $?"; tail -3 "$OUT/spend.log" 2>/dev/null
