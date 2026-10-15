#!/bin/bash
# Interleaved tier-2 chunks. Stops before a chunk if STOP exists or the cumulative estimate >= CAP.
export CFG=${CFG:?}
cd /Users/kc/Documents/sheepdog-vx
R=docs/bench/results
CAP=${CAP:-25}
EXP1=explain-en-flaky-test,explain-zh-input-rules,explain-control-en-done
EXP2=explain-zh-notes-ledger,explain-en-invoice-job,explain-control-zh-busy
WD1=workday-morning-standup,workday-scale-ten,workday-zh-evening
WD2=workday-interruptions,workday-explain-deep,workday-long-haul
cum=0
for spec in "explain-1 explain $EXP1 3" "explain-2 explain $EXP2 3" "workday-1 workday $WD1 2" "workday-2 workday $WD2 2"; do
 set -- $spec
 for arm in a rl nc; do
  while [ -e /Users/kc/Documents/sheepdog-vx/STOP ]; do sleep 60; done
  if [ -e $R/2026-10-11-t2/$arm/$1/report.json ]; then continue; fi
  if python3 -c "import sys;sys.exit(0 if $cum>=$CAP else 1)"; then echo "cap reached cum=$cum"; exit 0; fi
  echo "$(date +%H:%M) start $arm $1"
  BUDGET=5 $R/2026-10-11-run-t2-chunk.sh $arm $1 $2 $3 $4
  t=$(grep -o 'total ≈ \$[0-9.]*' $R/2026-10-11-t2/$arm/$1/spend.log 2>/dev/null | tail -1 | grep -o '[0-9.]*$')
  t=${t:-0}
  cum=$(python3 -c "print($cum+$t)")
  echo "$(date +%H:%M) done $arm $1 chunk=\$$t cum=\$$cum"
  echo "$arm $1 $t" >> $R/2026-10-11-t2/spend-chunks.txt
  git add -A docs/bench/results && git commit -qm "eval(bench): tier-2 $arm $1 vertex 2026-10-11" && git push -q origin eval/vertex-2026-10-11 2>&1 | tail -2
  sleep 60
 done
done
echo ALLDONE cum=$cum
