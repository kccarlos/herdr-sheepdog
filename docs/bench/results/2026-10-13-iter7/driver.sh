#!/bin/bash
# Interleaved comparison runs (STATUS.md step 2).
export S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/it7
cd "$(dirname "$0")"
for c in "main c1" "fix c1" "main c2" "fix c2"; do
  set -- $c
  ./run-chunk.sh $1 $2 unanswered 'ua-' 3
  bash "$S/bal.sh"
done
echo DONE
