#!/bin/bash
# usage: judge.sh ARM [FILTER]
# Sonnet rejudge of ARM/merged (run level, segments, orchestrator items); resumes where it stopped.
REPO=${REPO:?}; BIN=${BIN:?}
cd $REPO
R=docs/bench/results/2026-10-12-nc
D=$R/$1/merged
F=${2:+--filter $2}
$BIN bench rejudge --from $D --judge claude --judge-model sonnet --orchestrator $F >> $R/$1/rejudge.log 2>&1
echo "rejudge exit $?" >> $R/$1/rejudge.log
