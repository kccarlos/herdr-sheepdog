#!/usr/bin/env python3
"""Iteration 7 metrics from the Sonnet judge (ansjudge.py): answered later on targets, stale
follow-ups on controls and targets, per arm, with Newcombe 95 % CIs for fix − main.
usage: summarize.py   (reads judge-{main,fix}-c{1,2}.jsonl)"""
import json, math, collections, glob

def wilson(k, n, z=1.96):
    if n == 0: return (0, 0)
    p = k / n; d = 1 + z*z/n; c = p + z*z/(2*n); h = z*math.sqrt(p*(1-p)/n + z*z/(4*n*n))
    return ((c-h)/d, (c+h)/d)

def newcombe(k1, n1, k2, n2):
    """CI of p2 - p1 (Newcombe hybrid score)."""
    p1, p2 = k1/n1, k2/n2; l1, u1 = wilson(k1, n1); l2, u2 = wilson(k2, n2)
    d = p2 - p1
    return d, d - math.sqrt((p2-l2)**2 + (u1-p1)**2), d + math.sqrt((u2-p2)**2 + (p1-l1)**2)

rows = {"main": [], "fix": []}
for arm in rows:
    for f in sorted(glob.glob(f"judge-{arm}-c*.jsonl")):
        rows[arm] += [json.loads(l) for l in open(f)]

def count(arm, kind, key, sub=None):
    rs = [r for r in rows[arm] if r["kind"] == kind and (sub is None or sub in r["scenario"])]
    errs = sum(1 for r in rs if "answered_later" not in r["verdict"])
    k = sum(1 for r in rs if r["verdict"].get(key))
    return k, len(rs), errs

def line(name, kind, key, sub=None):
    km, nm, em = count("main", kind, key, sub); kf, nf, ef = count("fix", kind, key, sub)
    d, lo, hi = newcombe(km, nm, kf, nf)
    err = f"  (judge errors {em}/{ef})" if em or ef else ""
    print(f"{name:38s} main {km}/{nm} ({100*km/nm:.0f} %)  fix {kf}/{nf} ({100*kf/nf:.0f} %)  diff {100*d:+.0f} pp (95 % CI {100*lo:+.0f} to {100*hi:+.0f}){err}")

print("Targeted metric: answered later (targets)")
line("all targets", "target", "answered_later")
for case in ("fail", "cut", "unvoiced"):
    line(f"  {case}", "target", "answered_later", f"-{case}-")
for lang in ("-en", "-zh"):
    line(f"  {lang[1:]}", "target", "answered_later", lang)
print("\nNoise guard: stale follow-ups")
line("controls (dropped + reasked)", "dropped", "stale_followup") if False else None
for kind in ("dropped", "reasked", "target"):
    line(f"  {kind}", kind, "stale_followup")
km = sum(count("main", k, "stale_followup")[0] for k in ("dropped", "reasked")); nm = sum(count("main", k, "stale_followup")[1] for k in ("dropped", "reasked"))
kf = sum(count("fix", k, "stale_followup")[0] for k in ("dropped", "reasked")); nf = sum(count("fix", k, "stale_followup")[1] for k in ("dropped", "reasked"))
d, lo, hi = newcombe(km, nm, kf, nf)
print(f"{'controls pooled':38s} main {km}/{nm} ({100*km/nm:.0f} %)  fix {kf}/{nf} ({100*kf/nf:.0f} %)  diff {100*d:+.0f} pp (95 % CI {100*lo:+.0f} to {100*hi:+.0f})")
print("\nWhere it was first answered (user line after which), targets")
for arm in rows:
    c = collections.Counter(r["verdict"].get("first_line") for r in rows[arm] if r["kind"] == "target" and r["verdict"].get("answered_later"))
    print(f"  {arm}: {dict(sorted(c.items()))}")
