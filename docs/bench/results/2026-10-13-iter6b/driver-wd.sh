#!/bin/bash
# Workday gate, ×5 per arm, main vs fix (STATUS.md): w1 ×3 then w2 ×2, the two arms side by side.
export S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/it6b
export BUDGET=4
cd "$(dirname "$0")"
for c in "w1 3" "w2 2"; do
  set -- $c
  ./run-chunk.sh main $1 workday '' $2 & ./run-chunk.sh fix $1 workday '' $2 & wait
  bash "$S/bal.sh"
done
echo DONE
