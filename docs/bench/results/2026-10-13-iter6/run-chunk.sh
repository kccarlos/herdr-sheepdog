#!/bin/bash
# usage: run-chunk.sh ARM(fix|main) CHUNK FILE(explain|workday|acceptance) SCENARIO-FILTER REPEAT
# tier 1: brain DeepSeek deepseek-flash, simulated user DeepSeek deepseek-flash (workday has adversarial content: kept off Google),
# judge fake (Sonnet rejudge later). Prompts are embedded in each binary (sd-main from main, sd-fix from the branch).
set -u
S=${S:?scratch dir with sd-fix, sd-main, cfg.toml, bal.sh}
cd /Users/kc/Documents/herdr-sheepdog-iter6
R=docs/bench/results/2026-10-13-iter6
out=$R/$1/$2; mkdir -p "$out"
"$S/sd-$1" eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash --judge fake \
  --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$S/cfg.toml" \
  --files "testdata/eval/sim/$3.json" --filter "$4" \
  --repeat "$5" --personas rotate --parallel 4 --budget-usd 1.5 --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $? $1 $2"; tail -4 "$out/stdout.log"
