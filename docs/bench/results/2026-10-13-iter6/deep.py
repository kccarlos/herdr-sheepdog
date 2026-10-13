#!/usr/bin/env python3
"""Look rate on the detail beats of workday-explain-deep (held out, untuned): beat 3 (why it only
fails sometimes), 4 (more detail: what would the fix change), 8 (an example of where the money went).
A user line tagged with one of these beats is answered by the next user-triggered brain turn.
usage: deep.py RUN_DIR...   (first 5 runs per arm: pass the dirs in order; KEEP=5)"""
import json, sys, os
LOOK = {"read_screen", "run_cli", "investigate", "recall"}; BEATS = {3, 4, 8}
keep = int(os.environ.get("KEEP", "5")); n = 0; rows = []
for rd in sys.argv[1:]:
    rep = json.load(open(os.path.join(rd, "report.json")))
    for run in sorted([x for x in rep["runs"] if x["scenario"] == "workday-explain-deep" and x["status"] != "not_run"], key=lambda x: x["repeat"]):
        if n >= keep: break
        n += 1
        ev = os.path.join(rd, "events", f"workday-explain-deep.r{run['repeat']}.jsonl")
        turns = []; cur = None
        for l in open(ev):
            o = json.loads(l); p = o.get("payload", {})
            if o["type"] == "brain.turn_started" and p.get("trigger") == "ceo": cur = {"at": o["at_ms"], "calls": []}; turns.append(cur); tid = p["turn_id"]
            elif o["type"] == "brain.tool_call" and cur and p.get("turn_id") == tid: cur["calls"].append(p["name"])
        seen = set()
        for t in run["transcript"]:
            if t["who"] == "ceo" and t.get("beat") in BEATS and t["beat"] not in seen:
                seen.add(t["beat"])
                nxt = [x for x in turns if x["at"] >= t["at_ms"] - 50]
                look = bool(nxt) and any(c in LOOK for c in nxt[0]["calls"])
                rows.append((rd, run["repeat"], t["beat"], look))
k = sum(1 for r in rows if not r[3])
print(f"explain-deep detail beats: no fresh look {k}/{len(rows)} over {n} runs")
for r in rows: print("  ", r)
