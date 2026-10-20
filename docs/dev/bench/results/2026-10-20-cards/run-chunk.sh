#!/bin/bash
# usage: run-chunk.sh ARM(main|cards) SCENARIO REPEAT
# Tier 1 workday, orchestrator and simulated user DeepSeek deepseek-flash, judge fake
# (Sonnet rejudges afterwards), no failover provider, chattiness chatty (the default).
# main: origin/main 80ccafd built from `git archive` (no show tool).
# cards: this branch, --screen alternate (about half the runs visible, by scenario and repeat).
set -u
S=${S:?scratch dir with sd-main, sd-branch, main-src, cfg-empty.toml}
WT=${WT:?the branch worktree}
R=$WT/docs/dev/bench/results/2026-10-20-cards
out=$R/$1/$2; mkdir -p "$out"
case $1 in
  main)  BIN=$S/cards/sd-main;   SRC=$S/cards/main-src; X="";;
  cards) BIN=$S/cards/sd-branch; SRC=$WT;               X="--screen alternate";;
esac
cd "$SRC"
"$BIN" eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash --judge fake $X \
  --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$S/cfg-empty.toml" \
  --files testdata/eval/sim/workday.json --filter "$2" \
  --repeat "$3" --personas rotate --parallel 3 --budget-usd 2 --events "$out/events" --out "$out" > "$out/stdout.log" 2>&1
echo "exit $? $1 $2"; tail -1 "$out/stdout.log"
