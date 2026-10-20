#!/bin/bash
# usage: driver.sh SCENARIO...   each scenario x3, the main and cards chunks side by side.
export S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad
export WT=/Users/kc/Documents/herdr-sheepdog/.claude/worktrees/agent-a24dffcf6b23de439
cd "$(dirname "$0")"
for sc in "$@"; do
  ./run-chunk.sh main "$sc" 3 & ./run-chunk.sh cards "$sc" 3 & wait
  bash "$S/opus/bal.sh"
done
echo DONE
