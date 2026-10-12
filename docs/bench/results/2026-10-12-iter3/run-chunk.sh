#!/bin/bash
# usage: run-chunk.sh ARM(fix|main) CHUNK SCENARIO REPEAT
# tier 1: brain DeepSeek deepseek-flash, simulated user Vertex gemini-3.5-flash, judge fake (Sonnet segment rejudge later)
set -u
S=${S:?scratch dir with sd-fix, sd-main, cfg.toml}
cd /Users/kc/Documents/sheepdog-it3
R=docs/bench/results/2026-10-12-iter3
out=$R/$1/$2; mkdir -p "$out"
"$S/sd-$1" eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
  --simuser vertex --simuser-model gemini-3.5-flash --judge fake \
  --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$S/cfg.toml" \
  --files testdata/eval/sim/workday.json --filter "$3" \
  --repeat "$4" --personas rotate --parallel 4 --budget-usd 1.5 --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $? $1 $2"; tail -4 "$out/stdout.log"
