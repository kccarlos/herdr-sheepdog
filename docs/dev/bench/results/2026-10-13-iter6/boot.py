#!/usr/bin/env python3
"""usage: boot.py MAIN_ROWS.json FIX_ROWS.json  — fix − main with a per-run cluster bootstrap (10,000 draws)."""
import json, sys, random
random.seed(6)
def clusters(p):
    c = {}
    for r in json.load(open(p)): c.setdefault((r["sid"], r["rep"], r.get("dir", "")), []).append(r)
    return list(c.values())
M, F = clusters(sys.argv[1]), clusters(sys.argv[2])
metrics = {"failed": lambda r: r["wrong"] or (r["restates"] and r["new"] == 0), "no fresh look": lambda r: not r["look"],
           "wrong": lambda r: r["wrong"], "restates": lambda r: r["restates"], "new facts": lambda r: r["new"]}
def val(cs, f):
    rs = [r for c in cs for r in c]; return sum(float(f(r)) for r in rs) / len(rs)
for name, f in metrics.items():
    d0 = val(F, f) - val(M, f); ds = []
    for _ in range(10000):
        ds.append(val([random.choice(F) for _ in F], f) - val([random.choice(M) for _ in M], f))
    ds.sort(); lo, hi = ds[250], ds[9749]
    unit = "" if name == "new facts" else " pp"; k = 1 if name == "new facts" else 100
    print(f"{name:14s} main {val(M,f)*k:.2f} fix {val(F,f)*k:.2f}  diff {d0*k:+.2f}{unit} (95% CI {lo*k:+.2f} to {hi*k:+.2f})")
