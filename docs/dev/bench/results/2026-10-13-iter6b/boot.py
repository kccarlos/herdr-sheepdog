#!/usr/bin/env python3
"""usage: boot.py A_ROWS.json B_ROWS.json — B − A with a per-run cluster bootstrap (10,000 draws), on analyze.py's rows."""
import json, sys, random
random.seed(66)
def clusters(p):
    c = {}
    for r in json.load(open(p)):
        if r["judged"]: c.setdefault((r["sid"], r["rep"], r["dir"]), []).append(r)
    return list(c.values())
A, B = clusters(sys.argv[1]), clusters(sys.argv[2])
metrics = {"restates (PRIMARY)": (lambda r: r["restates"], 100), "new facts / episode": (lambda r: r["new_ep"], 1),
           "new facts / line": (lambda r: r["new"], 1), "wrong (episode)": (lambda r: r["wrong_ep"], 100),
           "failed (iter6)": (lambda r: r["wrong"] or (r["restates"] and r["new"] == 0), 100),
           "no fresh look": (lambda r: not r["look"], 100), "offers": (lambda r: r["offer"], 100), "asked": (lambda r: r["asked"], 100)}
def val(cs, f):
    rs = [r for c in cs for r in c]; return sum(float(f(r)) for r in rs) / max(1, len(rs))
for name, (f, k) in metrics.items():
    d0 = val(B, f) - val(A, f); ds = sorted(val([random.choice(B) for _ in B], f) - val([random.choice(A) for _ in A], f) for _ in range(10000))
    u = " pp" if k == 100 else ""
    print(f"{name:20s} {val(A,f)*k:6.2f} -> {val(B,f)*k:6.2f}  diff {d0*k:+.2f}{u} (95% CI {ds[250]*k:+.2f} to {ds[9749]*k:+.2f})")
