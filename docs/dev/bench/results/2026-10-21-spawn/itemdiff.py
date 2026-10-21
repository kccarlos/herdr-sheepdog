#!/usr/bin/env python3
"""usage: itemdiff.py A_DIR B_DIR ITEM...  segment-level yes rate of each judge item in two
merged eval sim results, and B - A with a 95 % cluster bootstrap CI (runs resampled within each arm)."""
import json, random, sys
def per_run(d, item):
    out = []
    for r in json.load(open(d + "/report.json"))["runs"]:
        y = n = 0
        for s in r.get("segments") or []:
            for v in s.get("verdicts") or []:
                if v["name"] == item or (item.endswith("*") and v["name"].startswith(item[:-1])):
                    if v.get("answer") in ("yes", "no"):
                        n += 1; y += v["answer"] == "yes"
        out.append((y, n))
    return out
def rate(rs): 
    n = sum(x[1] for x in rs); return sum(x[0] for x in rs) / n if n else float("nan")
a, b, items = sys.argv[1], sys.argv[2], sys.argv[3:]
random.seed(1)
for it in items:
    A, B = per_run(a, it), per_run(b, it)
    ds = sorted(rate(random.choices(B, k=len(B))) - rate(random.choices(A, k=len(A))) for _ in range(4000))
    print("| %s | %d/%d (%.0f %%) | %d/%d (%.0f %%) | %+.0f pp (%+.0f to %+.0f) |" % (it, sum(x[0] for x in A), sum(x[1] for x in A), 100*rate(A), sum(x[0] for x in B), sum(x[1] for x in B), 100*rate(B), 100*(rate(B)-rate(A)), 100*ds[100], 100*ds[3899]))
