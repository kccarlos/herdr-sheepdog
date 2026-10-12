#!/bin/bash
# usage: driver.sh "ARM CHUNK SCENARIO REPEAT" ...   runs chunks in order; stops when the DeepSeek balance < 32 CNY
set -u
R=/Users/kc/Documents/sheepdog-it3/docs/bench/results/2026-10-12-iter3
for spec in "$@"; do
  set -- $spec
  [ -e "$R/$1/$2/report.json" ] && continue
  bal=$("$S/bal.sh"); cny=$(echo "$bal" | grep -o 'CNY [0-9.]*' | cut -d' ' -f2)
  echo "$(date +%H:%M:%S) balance $bal before $spec"
  if python3 -c "import sys;sys.exit(0 if float('${cny:-0}')<32 else 1)"; then echo "STOP: balance below 32 CNY"; exit 1; fi
  "$R/run-chunk.sh" "$@"
done
echo "$(date +%H:%M:%S) balance $("$S/bal.sh") ALLDONE"
