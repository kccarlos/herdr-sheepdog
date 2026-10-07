#!/bin/bash
for sc in workday-scale-ten workday-adversarial-afternoon workday-zh-evening workday-interruptions workday-explain-deep workday-long-haul; do
  docs/bench/results/2026-10-07-chatty/run-chunk.sh $sc
done
