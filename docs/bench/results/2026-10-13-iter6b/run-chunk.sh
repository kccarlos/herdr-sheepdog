#!/bin/bash
# usage: run-chunk.sh ARM(main|iter6|b|fix) CHUNK FILE(explain|workday) SCENARIO-FILTER REPEAT
# tier 1: brain DeepSeek deepseek-flash (--fallback-provider none), simulated user DeepSeek deepseek-flash
# (workday has adversarial content: kept off Google), judge fake (the Sonnet judges run afterwards).
# Prompts are embedded in each binary: sd-main = main e04ace5 + the eval harness (799fe88),
# sd-iter6 = iteration 6's RULES.md + harness, sd-b = iter6 + B + harness (799fe88), sd-fix = A + B (952fdd5).
set -u
S=${S:?scratch dir with the sd-* binaries and cfg.toml}
cd /Users/kc/Documents/herdr-sheepdog-6b
R=docs/bench/results/2026-10-13-iter6b
out=$R/$1/$2; mkdir -p "$out"
"$S/sd-$1" eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash --judge fake \
  --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$S/cfg.toml" \
  --files "testdata/eval/sim/$3.json" --filter "$4" \
  --repeat "$5" --personas rotate --parallel 4 --budget-usd ${BUDGET:-2} --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $? $1 $2"; tail -4 "$out/stdout.log"
