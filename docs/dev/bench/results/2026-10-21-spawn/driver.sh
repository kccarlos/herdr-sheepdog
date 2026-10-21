#!/bin/bash
# usage: driver.sh ARM   the 6 non-adversarial workday scenarios x3, two scenarios at a time.
cd "$(dirname "$0")"
run() { ./run-chunk.sh "$1" "$2" 3; }
A=$1
run $A workday-explain-deep & run $A workday-interruptions & wait
run $A workday-long-haul & run $A workday-morning-standup & wait
run $A workday-scale-ten & run $A workday-zh-evening & wait
echo DONE $A
