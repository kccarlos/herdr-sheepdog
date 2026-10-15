#!/bin/bash
# Interleaved tier-2 chunks, arms a and nc. Before each chunk: stop if STOP exists, the
# cumulative bench-meter estimate >= CAP, or the DeepSeek balance < 32 CNY.
export REPO=${REPO:?} BIN=${BIN:?} CFG=${CFG:?}
cd $REPO
R=docs/bench/results/2026-10-12-nc
CAP=${CAP:-25}
EXP1=explain-en-flaky-test,explain-zh-input-rules,explain-control-en-done
EXP2=explain-zh-notes-ledger,explain-en-invoice-job,explain-control-zh-busy
WD1=workday-morning-standup,workday-scale-ten,workday-zh-evening
WD2=workday-interruptions,workday-explain-deep,workday-long-haul
cum=0
[ -e $R/spend-chunks.txt ] && cum=$(awk '{s+=$3} END{print s+0}' $R/spend-chunks.txt)
for spec in "explain-1 explain $EXP1 5" "explain-2 explain $EXP2 5" "workday-1 workday $WD1 2" "workday-2 workday $WD2 2"; do
 set -- $spec
 for arm in a nc; do
  [ -e $REPO/STOP ] && { echo "STOP file"; exit 0; }
  if [ -e $R/$arm/$1/report.json ]; then continue; fi
  if python3 -c "import sys;sys.exit(0 if $cum>=$CAP else 1)"; then echo "cap reached cum=$cum"; exit 0; fi
  bal=$(set -a; . /Users/kc/Documents/herdr-sheepdog/.env; set +a; curl -s -H "Authorization: Bearer $DEEPSEEK_API_KEY" https://api.deepseek.com/user/balance | python3 -c "import sys,json;d=json.load(sys.stdin);print([b['total_balance'] for b in d['balance_infos'] if b['currency']=='CNY'][0])")
  echo "$(date +%H:%M) deepseek balance $bal CNY"
  if python3 -c "import sys;sys.exit(0 if float('${bal:-0}')<32 else 1)"; then echo "deepseek balance below 32 CNY, stopping"; exit 0; fi
  echo "$(date +%H:%M) start $arm $1"
  BUDGET=6 $R/run-chunk.sh $arm $1 $2 $3 $4
  t=$(grep -o 'total ≈ \$[0-9.]*' $R/$arm/$1/spend.log 2>/dev/null | tail -1 | grep -o '[0-9.]*$')
  t=${t:-0}
  cum=$(python3 -c "print(round($cum+$t,2))")
  echo "$(date +%H:%M) done $arm $1 chunk=\$$t cum=\$$cum"
  echo "$arm $1 $t" >> $R/spend-chunks.txt
  git add -A $R && git commit -qm "eval(bench): tier-2 nc confirmation $arm $1 (2026-10-12)" && git push -q origin iter/1-nc 2>&1 | tail -2
  sleep 60
 done
done
echo ALLDONE cum=$cum
