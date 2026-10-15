#!/bin/zsh
cd /Users/kc/Documents/sheepdog-facts
BIN=$SCRATCH/sheepdog-facts
COMMON=(bench run --tier 2 --env-file /Users/kc/Documents/herdr-sheepdog/.env --brain-provider vertex --brain-model gemini-3.5-flash --parallel 3 --ports 7790,7791,7792)
H1=$(sed -n 1p $SCRATCH/ab/ids.txt); H2=$(sed -n 2p $SCRATCH/ab/ids.txt)
arm() { # name promptsdir filter budget
  echo "$(date +%T) start $1" >> $SCRATCH/ab/progress.log
  $BIN $COMMON --prompts-dir $2 --filter $3 --repeat 1 --budget-usd $4 --out $SCRATCH/ab/$1 > $SCRATCH/ab/$1.log 2>&1
  echo "$(date +%T) done $1 rc=$?" >> $SCRATCH/ab/progress.log
}
for r in 1 2 3; do
  arm facts-old-r$r $SCRATCH/prompts-old facts 2
  arm facts-new-r$r $SCRATCH/prompts-new facts 2
done
arm suite-old-h1 $SCRATCH/prompts-old $H1 3.5
arm suite-new-h1 $SCRATCH/prompts-new $H1 3.5
arm suite-new-h2 $SCRATCH/prompts-new $H2 3.5
arm suite-old-h2 $SCRATCH/prompts-old $H2 3.5
echo "$(date +%T) ALL DONE" >> $SCRATCH/ab/progress.log
