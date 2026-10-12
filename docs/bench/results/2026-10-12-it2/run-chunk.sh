#!/bin/bash
# usage: run-chunk.sh ARM K
# Tier 2, production config: brain DeepSeek deepseek-flash, voice Vertex Live gemini-3.8-live,
# Vertex TTS, chattiness chatty, simulated user Vertex gemini-3.5-flash, two scratch daemons
# on :7921/:7922, judge fake (judged later with Sonnet). One chunk = the 6 non-adversarial
# workday scenarios once, with the personas `--personas rotate` gives repeat K (the list
# rotated by K-1, so PersonaFor(list, id, 1) equals rotate's repeat K); merge.py renumbers
# the runs to rK.
set -u
ARM=$1; K=$2
REPO=${REPO:?}; CFG=${CFG:?}
case $ARM in fix) BIN=${BIN_FIX:?};; main) BIN=${BIN_MAIN:?};; esac
case $K in 1) P=terse,rambling,zh,impatient,vague;; 2) P=rambling,zh,impatient,vague,terse;; 3) P=zh,impatient,vague,terse,rambling;; esac
IDS=workday-morning-standup,workday-scale-ten,workday-zh-evening,workday-interruptions,workday-explain-deep,workday-long-haul
OUT=$REPO/docs/bench/results/2026-10-12-it2/$ARM/r$K
mkdir -p "$OUT"
cd $REPO
$BIN bench run --tier 2 --env-file /Users/kc/Documents/herdr-sheepdog/.env \
  --config "$CFG" --scenarios testdata/bench/scenarios/workday.json --filter "$IDS" \
  --brain-provider deepseek --brain-model deepseek-flash \
  --simuser vertex --simuser-model gemini-3.5-flash --judge fake \
  --ports 7921,7922 --parallel 2 --repeat 1 --personas "$P" \
  --budget-usd "${BUDGET:-5}" --out "$OUT" > "$OUT.stdout.log" 2>&1
echo "exit $?"; tail -3 "$OUT/spend.log" 2>/dev/null
