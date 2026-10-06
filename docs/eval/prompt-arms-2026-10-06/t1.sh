#!/bin/bash
# usage: t1.sh ARM OUTDIR [eval sim flags...]   ARM in a0 a b c
set -u
SP=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad
arm=$1; out=$2; shift 2
. $SP/env.sh
unset GEMINI_API_KEY GEMINI_API_KEY_1 GEMINI_API_KEY_2 GEMINI_API_KEY_3
case $arm in
  a0) bin=$SP/sd-main; pd=() ;;
  a)  bin=$SP/sd-b; pd=(--prompts-dir /Users/kc/Documents/sheepdog-b/docs/eval/prompt-arms/a) ;;
  b)  bin=$SP/sd-b; pd=(--prompts-dir /Users/kc/Documents/sheepdog-b/prompts) ;;
  c)  bin=$SP/sd-b; pd=(--prompts-dir /Users/kc/Documents/sheepdog-b/docs/eval/prompt-arms/c) ;;
  *) echo "bad arm"; exit 2 ;;
esac
cd /Users/kc/Documents/sheepdog-arms
mkdir -p "$out"
exec "$bin" eval sim --live --provider deepseek --model deepseek-flash --fallback-provider none \
  --simuser deepseek --simuser-model deepseek-flash ${pd[@]+"${pd[@]}"} --events "$out/events" --out "$out" "$@"
