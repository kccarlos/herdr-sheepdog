#!/bin/bash
# usage: driver.sh SCENARIO...   each scenario x3, Opus and DeepSeek chunks side by side.
# Run under one `op run` (ANTHROPIC_API_KEY from 1Password).
export S=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad/opus
cd "$(dirname "$0")"
for sc in "$@"; do
  ./run-chunk.sh opus-medium "$sc" 3 & ./run-chunk.sh deepseek "$sc" 3 & wait
  bash "$S/bal.sh"
  sleep 30
done
echo DONE
