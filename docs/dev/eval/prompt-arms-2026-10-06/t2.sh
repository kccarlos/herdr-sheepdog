#!/bin/bash
# usage: t2.sh ARM OUTDIR [bench run flags...]   ARM in a b c (sd-b binary)
set -u
SP=/private/tmp/claude-501/-Users-kc-Documents-herdr-sheepdog/dda19d26-9a6f-43a3-b88b-66f64e63940c/scratchpad
arm=$1; out=$2; shift 2
. $SP/env.sh
case $arm in
  a)  pd=/Users/kc/Documents/sheepdog-b/docs/eval/prompt-arms/a ;;
  b)  pd=/Users/kc/Documents/sheepdog-b/prompts ;;
  c)  pd=/Users/kc/Documents/sheepdog-b/docs/eval/prompt-arms/c ;;
  *) echo "bad arm"; exit 2 ;;
esac
cd /Users/kc/Documents/sheepdog-arms
mkdir -p "$out" "$SP/scratch-$arm"
exec "$SP/sd-b" bench run --tier 2 --say --brain-provider deepseek --brain-model deepseek-flash \
  --simuser deepseek --simuser-model deepseek-flash --prompts-dir "$pd" \
  --scratch "$SP/scratch-$arm" --out "$out" "$@"
