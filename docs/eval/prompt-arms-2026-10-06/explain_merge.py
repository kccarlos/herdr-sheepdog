#!/usr/bin/env python3
"""Explain metrics per arm over the first-night runs plus the top-up runs.

Per arm: the runs with an explain score (interrupted / not-run runs have
none), mean orchestrator key-point coverage and comprehension, follow-up
depth and no-padding counts; B - A0 coverage difference with a bootstrap
95 % CI over runs (2000 resamples, fixed seed).

usage: explain_merge.py RESULTS_DIR ARM [ARM ...]   (RESULTS_DIR/t1-ARM/explain*/report.json)
"""
import glob, json, random, sys

root, arms = sys.argv[1], sys.argv[2:]
data = {}
for a in arms:
    runs, scored = [], []
    for f in sorted(glob.glob(f"{root}/t1-{a}/explain*/report.json")):
        for r in json.load(open(f))["runs"]:
            e = r.get("explain")
            if e and e.get("orchestrator"):
                scored.append(e["orchestrator"])  # control cases: padding only
                if e["orchestrator"].get("coverage") is not None:
                    runs.append(e["orchestrator"])
    data[a] = runs
    n = len(runs)
    cov = sum(o["coverage"] for o in runs) / n
    comp = [o["comprehension"] for o in runs if o.get("comprehension") is not None]
    da = sum(o.get("depth_asked", 0) for o in runs); dp = sum(o.get("depth_passed", 0) for o in runs)
    pa = sum(o.get("padding_asked", 0) for o in scored); pp = sum(o.get("padding_passed", 0) for o in scored)
    print(f"{a}: scored runs {len(scored)}, substantive {n}, coverage {cov:.2f}, comprehension {sum(comp)/len(comp):.2f}, follow-up depth {dp}/{da}, no padding {pp}/{pa}")
rnd = random.Random(1)
def mean(x): return sum(x) / len(x)
for a in arms:
    for b in arms:
        if a >= b: continue
        xa = [o["coverage"] for o in data[a]]; xb = [o["coverage"] for o in data[b]]
        d = sorted(mean([rnd.choice(xb) for _ in xb]) - mean([rnd.choice(xa) for _ in xa]) for _ in range(2000))
        print(f"coverage {b} - {a}: {mean(xb)-mean(xa):+.2f} (95 % CI {d[50]:+.2f} to {d[1949]:+.2f})")
