#!/usr/bin/env python3
"""Guards and diagnostics for iteration 6 (explain runs). usage: guards.py SCENARIO_FILE RUN_DIR..."""
import json, sys, os, glob, re
specs = {s["id"]: s for s in json.load(open(sys.argv[1]))["scenarios"] if s.get("explain")}
from collections import Counter
calls = Counter(); det_dur = []; ctl_reads = 0; ctl_turns = 0; fallbacks = 0; pend = 0; narr = []
NARR = re.compile(r"(digging|sent it back|is looking into|is tracing|still tracing|asked it to look|让它(去)?查|它正在查)", re.I)
for rd in sys.argv[2:]:
    rep = json.load(open(os.path.join(rd, "report.json")))
    for run in rep["runs"]:
        for t in run["transcript"]:
            if t["who"] in ("sheepdog", "push") and NARR.search(t["text"]): narr.append((rd, run["scenario"], run["repeat"], t["text"][:160]))
    for ev in sorted(glob.glob(os.path.join(rd, "events", "*.jsonl"))):
        sid = os.path.basename(ev).split(".r")[0]
        if sid not in specs: continue
        kinds = specs[sid]["explain"]["turns"]; order = []; tt = {}
        for l in open(ev):
            o = json.loads(l); p = o.get("payload", {})
            if o["type"] == "brain.turn_started" and p.get("trigger") == "ceo": order.append(p["turn_id"]); tt[p["turn_id"]] = {"calls": [], "dur": None, "fb": False}
            elif o["type"] == "brain.tool_call" and p.get("turn_id") in tt: tt[p["turn_id"]]["calls"].append(p["name"] + ":" + str(p.get("args", {}).get("source", "")))
            elif o["type"] == "brain.turn" and p.get("turn_id") in tt: tt[p["turn_id"]]["dur"] = p.get("dur_ms"); tt[p["turn_id"]]["fb"] = p.get("fallback")
        for i, k in enumerate(kinds):
            if i >= len(order): break
            t = tt[order[i]]
            if k in ("then", "more", "why", "example"):
                for c in t["calls"]: calls[c] += 1
                if t["dur"] is not None: det_dur.append(t["dur"])
                fallbacks += bool(t["fb"]); pend += any(c.startswith("investigate") for c in t["calls"])
            if k == "simple":
                ctl_turns += 1; ctl_reads += sum(1 for c in t["calls"] if c.split(":")[0] in ("read_screen", "run_cli", "investigate", "recall"))
det_dur.sort()
print("detail-turn tool calls:", dict(calls))
print("detail turns with investigate:", pend, "fallbacks:", fallbacks)
if det_dur: print("detail-turn dur_ms median %d p90 %d max %d (n=%d)" % (det_dur[len(det_dur)//2], det_dur[int(len(det_dur)*.9)], det_dur[-1], len(det_dur)))
print("control turns:", ctl_turns, "reads on them:", ctl_reads)
print("worker-is-looking narration lines:", len(narr))
for n in narr: print("  ", n)
