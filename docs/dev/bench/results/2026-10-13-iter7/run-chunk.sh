#!/bin/bash
# usage: run-chunk.sh ARM(fix|main) CHUNK FILE(unanswered|workday) SCENARIO-FILTER REPEAT
# tier 1: brain DeepSeek deepseek-flash (--fallback-provider none), simulated user DeepSeek deepseek-flash
# (workday has adversarial content: kept off Google; unanswered.json is fixed scripts, no simulated user),
# judge fake (the Sonnet judges run afterwards). sd-main = main + the eval fault harness (caa239f),
# sd-fix = the branch (51a9c99 or later).
set -u
S=${S:?scratch dir with sd-fix, sd-main, cfg.toml}
cd /Users/kc/Documents/sheepdog-iter7
R=docs/bench/results/2026-10-13-iter7
out=$R/$1/$2; mkdir -p "$out"
"$S/sd-$1" eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash --judge fake \
  --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$S/cfg.toml" \
  --files "testdata/eval/sim/$3.json" --filter "$4" \
  --repeat "$5" --personas rotate --parallel 4 --budget-usd ${BUDGET:-4} --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $? $1 $2"; tail -4 "$out/stdout.log"
