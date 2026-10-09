#!/bin/bash
# usage: run-chunk.sh SCENARIO_ID...   tier 1, x5, one scenario after another
set -u
export DEEPSEEK_API_KEY=injected-by-proxy
R=docs/bench/results/2026-10-08-rebaseline
for sc in "$@"; do
  out=$R/parts/$sc; mkdir -p "$out"
  /tmp/sheepdog eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
    --simuser deepseek --simuser-model deepseek-flash --judge fake \
    --files testdata/eval/sim/workday.json --filter "$sc" \
    --repeat 5 --personas rotate --parallel 5 --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
done
