#!/bin/bash
# usage: run-chunk.sh SCENARIO_ID   runs arms r and rl (concurrently) for one workday scenario x5, tier 1
set -u
export DEEPSEEK_API_KEY=injected-by-proxy
sc=$1; R=docs/bench/results/2026-10-07-chatty
for arm in r rl; do
  out=$R/chunks/$arm/$sc; mkdir -p "$out"
  /tmp/sheepdog-b eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
    --simuser deepseek --simuser-model deepseek-flash --judge fake \
    --prompts-dir docs/eval/prompt-arms/$arm --files testdata/eval/sim/workday.json --filter "$sc" \
    --repeat 5 --personas rotate --parallel 5 --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1 &
done
wait
