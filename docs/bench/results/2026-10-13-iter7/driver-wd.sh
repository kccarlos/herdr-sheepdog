#!/bin/bash
# Workday gate, ×5 per arm, interleaved (STATUS.md step 2).
export S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/it7
export BUDGET=6
cd "$(dirname "$0")"
for c in "main w1 3" "fix w1 3" "main w2 2" "fix w2 2"; do
  set -- $c
  ./run-chunk.sh $1 $2 workday '' $3
  bash "$S/bal.sh"
done
echo DONE
