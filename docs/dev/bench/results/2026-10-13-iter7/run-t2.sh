#!/bin/bash
# usage: run-t2.sh BIN-NAME(sd-main|sd-fix2) OUTNAME REPEAT
# Tier 2 check of the voice-layer hooks (benign scenarios only; Vertex Live voice, Vertex
# simulated user, DeepSeek orchestrator), two scratch daemons on :7921/:7922.
set -u
S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/it7
cd /Users/kc/Documents/sheepdog-iter7
OUT=docs/bench/results/2026-10-13-iter7/t2/$2; mkdir -p "$OUT"
"$S/$1" bench run --tier 2 --env-file /Users/kc/Documents/herdr-sheepdog/.env --config "$S/cfg.toml" \
  --scenarios 'testdata/bench/scenarios/*.json' --filter "barge-in-long-answer,barge-in-hold-to-talk,slowness-brain-9s" \
  --brain-provider deepseek --brain-model deepseek-flash --simuser vertex --simuser-model gemini-3.5-flash --judge fake \
  --ports 7921,7922 --parallel 2 --repeat "$3" --budget-usd 3 --out "$OUT" > "$OUT.stdout.log" 2>&1
echo "exit $? $1 $2"; tail -2 "$OUT/spend.log"
