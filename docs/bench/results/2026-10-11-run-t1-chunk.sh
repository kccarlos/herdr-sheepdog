#!/bin/bash
# usage: run-t1-chunk.sh SCENARIO_ID   tier 1, Gemini 3.5 flash on Vertex, x5, simulated user on DeepSeek, judge fake
set -u
cd /Users/kc/Documents/sheepdog-vx
R=docs/bench/results/2026-10-11-t1-gemini
out=$R/parts/$1; mkdir -p "$out"
/tmp/sheepdog-vx eval sim --live --provider vertex --model gemini-3.5-flash --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash --judge fake --env-file /Users/kc/Documents/herdr-sheepdog/.env \
  --config "$CFG" --files testdata/eval/sim/workday.json --filter "$1" \
  --repeat 5 --personas rotate --parallel 3 --budget-usd "${BUDGET:-1}" --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $?"; tail -6 "$out/stdout.log"
