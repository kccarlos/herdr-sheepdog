#!/bin/bash
# usage: rejudge.sh ARM   Sonnet run-level and segment rejudge of ARM/merged (branch binary for both arms,
# so the rubric and rendering are the same; main records simply have no screen lines).
S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad
WT=/Users/kc/Documents/herdr-sheepdog/.claude/worktrees/agent-a24dffcf6b23de439
R=$WT/docs/dev/bench/results/2026-10-20-cards
cd "$WT"
"$S/cards/sd-branch" bench rejudge --from "$R/$1/merged" --judge claude --judge-model sonnet > "$R/rejudge-$1.log" 2>&1
echo "exit $? $1"
