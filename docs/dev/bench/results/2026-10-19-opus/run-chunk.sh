#!/bin/bash
# usage: run-chunk.sh ARM(opus-medium|deepseek) SCENARIO REPEAT
# tier 1 workday, simulated user DeepSeek deepseek-flash, judge fake (Sonnet rejudges afterwards),
# no failover provider. opus-medium: Claude Opus 5.5, [anthropic] effort medium / event_effort low
# (the adapter's defaults). deepseek: deepseek-flash, the control. Binary: main 963f46c.
set -u
S=${S:?scratch dir with sd, cfg-*.toml}
cd /Users/kc/Documents/herdr-sheepdog
R=docs/dev/bench/results/2026-10-19-opus
out=$R/$1/$2; mkdir -p "$out"
case $1 in
  opus-medium) P="--provider anthropic --model claude-opus-5-5"; C=$S/cfg-medium.toml; B=${BUDGET_OPUS:-6};;
  deepseek)    P="--provider deepseek --model deepseek-flash";   C=$S/cfg-medium.toml; B=1;;
esac
"$S/sd" eval sim --live $P --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash --judge fake \
  --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$C" \
  --files testdata/eval/sim/workday.json --filter "$2" \
  --repeat "$3" --personas rotate --parallel 3 --budget-usd "$B" --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $? $1 $2"; tail -4 "$out/stdout.log"
