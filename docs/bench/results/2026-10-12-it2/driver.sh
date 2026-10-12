#!/bin/bash
# Interleaved tier-2 chunks fix, main (r1, r2, r3). Before each pair: stop if STOP exists,
# if the cumulative bench-meter estimate plus two chunks of the last size would pass CAP,
# or if the DeepSeek balance is below 32 CNY.
export REPO=${REPO:?} CFG=${CFG:?} BIN_FIX=${BIN_FIX:?} BIN_MAIN=${BIN_MAIN:?}
cd $REPO
R=docs/bench/results/2026-10-12-it2
CAP=${CAP:-20}
cum=0; last=4
[ -e $R/spend-chunks.txt ] && cum=$(awk '{s+=$3} END{print s+0}' $R/spend-chunks.txt)
for k in 1 2 3; do
 if python3 -c "import sys;sys.exit(0 if $cum+2*$last>$CAP else 1)"; then echo "cap: cum=$cum last=$last"; break; fi
 for arm in fix main; do
  [ -e $REPO/STOP ] && { echo "STOP file"; exit 0; }
  [ -e $R/$arm/r$k/report.json ] && continue
  bal=$(set -a; . /Users/kc/Documents/herdr-sheepdog/.env; set +a; curl -s -H "Authorization: Bearer $DEEPSEEK_API_KEY" https://api.deepseek.com/user/balance | python3 -c "import sys,json;d=json.load(sys.stdin);print([b['total_balance'] for b in d['balance_infos'] if b['currency']=='CNY'][0])")
  echo "$(date +%H:%M) deepseek balance $bal CNY"
  if python3 -c "import sys;sys.exit(0 if float('${bal:-0}')<32 else 1)"; then echo "deepseek balance below 32 CNY, stopping"; exit 0; fi
  echo "$(date +%H:%M) start $arm r$k"
  $R/run-chunk.sh $arm $k
  t=$(grep -o 'total ≈ \$[0-9.]*' $R/$arm/r$k/spend.log 2>/dev/null | tail -1 | grep -o '[0-9.]*$')
  t=${t:-0}; last=$t
  cum=$(python3 -c "print(round($cum+$t,2))")
  echo "$(date +%H:%M) done $arm r$k chunk=\$$t cum=\$$cum"
  echo "$arm r$k $t" >> $R/spend-chunks.txt
  git add -A $R && git commit -qm "eval(bench): tier-2 iteration 2 $arm r$k (2026-10-12)" && git push -q origin iter/2-voice-news 2>&1 | tail -2
  sleep 60
 done
done
echo ALLDONE cum=$cum
