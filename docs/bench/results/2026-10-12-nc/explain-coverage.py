#!/usr/bin/env python3
"""Explain coverage table, arm A vs arm B: per-run means over the explain
scenarios (controls excluded), 95 % CI by bootstrap over runs, B - A by an
independent bootstrap. usage: explain-coverage.py A_DIR B_DIR [names]"""
import glob, json, random, sys

def load(d):
    out = []
    for f in sorted(glob.glob(d + "/runs/explain-*.json")):
        r = json.load(open(f))
        if "control" in r["scenario"] or not r.get("explain"):
            continue
        out.append(r["explain"])
    return out

def measures(e):
    o, v = e.get("orchestrator") or {}, e.get("voice") or {}
    lang = e.get("language")
    def words(side):
        w = [x for x in (side.get("words") or []) if x is not None and x >= 0]
        return sum(w) / len(w) if w else None
    return {
        "Orchestrator coverage": (o.get("coverage"), 1),
        "Voice coverage": (v.get("coverage"), 1),
        "Voice share of the orchestrator's points": (v.get("of_orchestrator"), 1),
        "Voice ear comprehension": (v.get("comprehension"), 1),
        "Orchestrator ear comprehension": (o.get("comprehension"), 1),
        "Voice follow-up depth pass": (v.get("depth_passed"), v.get("depth_asked") or 0),
        "Orchestrator follow-up depth pass": (o.get("depth_passed"), o.get("depth_asked") or 0),
        "Voice words per answer, " + lang: (words(v), 1),
        "Orchestrator words per answer, " + lang: (words(o), 1),
        "Follow-ups answered without the orchestrator": (e.get("follow_ups_not_consulted"), None),
    }

def stat(rows, key):
    num = den = 0.0
    for m in rows:
        x, w = m.get(key, (None, 0))
        if x is None or (w is not None and w == 0):
            continue
        if w is None:
            num += x; den += 1
        elif key.endswith("depth pass"):
            num += x; den += w
        else:
            num += x; den += 1
    return num / den if den else None

def boot(rows, key, n=2000):
    vals = []
    for _ in range(n):
        s = stat([random.choice(rows) for _ in rows], key)
        if s is not None:
            vals.append(s)
    srt = sorted(vals)
    return srt[int(0.025 * len(srt))], srt[int(0.975 * len(srt)) - 1], vals

random.seed(1)
a, b = [measures(e) for e in load(sys.argv[1])], [measures(e) for e in load(sys.argv[2])]
na, nb = (sys.argv[3].split(",") if len(sys.argv) > 3 else ["A", "B"])
keys = []
for m in a + b:
    for k in m:
        if k not in keys:
            keys.append(k)
print(f"Explain runs (controls excluded): {na} {len(a)}, {nb} {len(b)}\n")
print(f"| Measure | {na} | {nb} | {nb} − {na} (95 % CI) |\n|---|---|---|---|")
for k in keys:
    sa, sb = stat(a, k), stat(b, k)
    if sa is None or sb is None:
        continue
    la, ha, va = boot(a, k); lb, hb, vb = boot(b, k)
    d = sorted(y - x for x, y in zip(va, vb))
    lo, hi = d[int(0.025 * len(d))], d[int(0.975 * len(d)) - 1]
    f = (lambda x: f"{x:.1f}") if "words" in k else (lambda x: f"{x:.2f}")
    print(f"| {k} | {f(sa)} ({f(la)}–{f(ha)}) | {f(sb)} ({f(lb)}–{f(hb)}) | {'+' if sb-sa>=0 else ''}{f(sb-sa)} ({f(lo)} to {f(hi)}) |")
