#!/bin/bash
# Explain ×5 per arm (e1 ×3, e2 ×2), then workday ×5 per arm (w1 ×3, w2 ×2); the two arms side by side.
export S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/it6t
cd "$(dirname "$0")"
for c in "e1 3" "e2 2"; do
  set -- $c
  ./run-chunk.sh main $1 explain 'explain-' $2 & ./run-chunk.sh fix $1 explain 'explain-' $2 & wait
  bash "$S/bal.sh"
done
export BUDGET=4
for c in "w1 3" "w2 2"; do
  set -- $c
  ./run-chunk.sh main $1 workday '' $2 & ./run-chunk.sh fix $1 workday '' $2 & wait
  bash "$S/bal.sh"
done
echo DONE
