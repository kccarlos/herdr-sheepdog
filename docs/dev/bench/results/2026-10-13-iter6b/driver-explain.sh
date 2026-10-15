#!/bin/bash
# Explain comparison, ×5 per arm, interleaved (STATUS.md): e1 ×3 then e2 ×2; two chunks at a time.
export S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/it6b
cd "$(dirname "$0")"
for c in "e1 3" "e2 2"; do
  set -- $c
  ./run-chunk.sh main $1 explain 'explain-' $2 & ./run-chunk.sh fix $1 explain 'explain-' $2 & wait
  ./run-chunk.sh b $1 explain 'explain-' $2 & ./run-chunk.sh iter6 $1 explain 'explain-' $2 & wait
  bash "$S/bal.sh"
done
echo DONE
