#!/usr/bin/env python3
"""Metric A of iteration 6: detail turns answered without a fresh look.
usage: looks.py SCENARIO_FILE RUN_DIR [RUN_DIR...]
A detail turn is a scripted line labelled then/more/why/example (explain block) or,
for workday scenarios, a beat whose line the scenario marks as a detail ask is not
used: only explain blocks count. The orchestrator turn answering the line is the
k-th utterance turn triggered by the user (fixed scripts: one turn per line).
A fresh look is any call of read_screen, run_cli, investigate or recall in that turn."""
import json, sys, glob, os, math
LOOK = {"read_screen", "run_cli", "investigate", "recall"}
DETAIL = {"then", "more", "why", "example"}

def wilson(k, n, z=1.96):
    if n == 0: return (0, 0)
    p = k / n; d = 1 + z*z/n
    c = (p + z*z/(2*n)) / d; h = z*math.sqrt(p*(1-p)/n + z*z/(4*n*n)) / d
    return (c - h, c + h)

def load_specs(path):
    d = json.load(open(path))
    return {s["id"]: s for s in d["scenarios"] if s.get("explain")}

def turns_of(events):
    turns = {}; order = []
    for l in open(events):
        o = json.loads(l); t = o["type"]; p = o.get("payload", {})
        if t == "brain.turn_started" and p.get("trigger") == "ceo":
            turns[p["turn_id"]] = {"calls": [], "kind": p.get("kind")}; order.append(p["turn_id"])
        elif t == "brain.tool_call" and p.get("turn_id") in turns:
            turns[p["turn_id"]]["calls"].append((p["name"], p.get("args", {}).get("source", "")))
    return [turns[i] for i in order]

def main():
    specs = load_specs(sys.argv[1])
    rows = []
    for rd in sys.argv[2:]:
        for ev in sorted(glob.glob(os.path.join(rd, "events", "*.jsonl"))):
            base = os.path.basename(ev); sid = base.split(".r")[0]
            if sid not in specs: continue
            kinds = specs[sid]["explain"]["turns"]
            ts = turns_of(ev)
            if len(ts) != len(kinds):
                print(f"# {rd} {base}: {len(ts)} user turns for {len(kinds)} lines; aligned by order", file=sys.stderr)
            for i, k in enumerate(kinds):
                if i >= len(ts): break
                look = [c for c in ts[i]["calls"] if c[0] in LOOK]
                rows.append((rd, base, i, k, bool(look), look))
    by = {}
    for r in rows:
        by.setdefault(r[3], [0, 0]); by[r[3]][1] += 1; by[r[3]][0] += (not r[4])
    for k in ["explain", "then", "more", "why", "example", "simple"]:
        if k in by:
            n0, n = by[k]; lo, hi = wilson(n0, n)
            print(f"{k:8s} no fresh look {n0:3d}/{n:3d} = {n0/n:5.0%} (95% CI {lo:.0%}-{hi:.0%})")
    d = [r for r in rows if r[3] in DETAIL]; n0 = sum(not r[4] for r in d); lo, hi = wilson(n0, len(d))
    print(f"DETAIL   no fresh look {n0:3d}/{len(d):3d} = {n0/max(1,len(d)):5.0%} (95% CI {lo:.0%}-{hi:.0%})")
    if os.environ.get("ROWS"):
        for r in rows: print(r[1], r[2], r[3], "LOOK" if r[4] else "none", r[5])

main()
