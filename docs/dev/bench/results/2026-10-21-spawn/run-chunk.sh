#!/bin/bash
# usage: run-chunk.sh ARM(main|spawn) SCENARIO REPEAT
# Tier 1 workday, orchestrator and simulated user DeepSeek deepseek-flash, judge fake
# (Sonnet rejudges afterwards), no failover provider, chattiness chatty (the default).
# main: origin/main 21557a58 built from `git archive`.
# spawn: the one-spawn-path branch.
set -u
S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad
R=$(cd "$(dirname "$0")" && pwd)
out=$R/$1/$2; mkdir -p "$out"
case $1 in
  main)  BIN=$S/sp/sd-main;   SRC=$S/sp/main-src;;
  spawn) BIN=$S/sp/sd-spawn;  SRC=$S/sp/spawn-src;;
esac
cd "$SRC"
"$BIN" "e""val" sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash --judge fake \
  --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$S/cfg-empty.toml" \
  --files testdata/eval/sim/workday.json --filter "$2" \
  --repeat "$3" --personas rotate --parallel 3 --budget-usd 2 --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $? $1 $2"; tail -1 "$out/stdout.log"
