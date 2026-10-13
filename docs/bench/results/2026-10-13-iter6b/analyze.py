#!/usr/bin/env python3
"""Iteration 6b metrics (pre-registered in STATUS.md).
usage: analyze.py SCENARIO_FILE FACTS.jsonl OUT_ROWS.json RUN_DIR [RUN_DIR...]
Joins the fact judge (one verdict per run, every user line numbered) with the events (one
orchestrator user turn per user line, in order). A detail line is a script line labelled
then/more/why/example; a user line tagged "answer" (offer_yes) belongs to the detail line before it.
Rows are one per detail line; prints the per-arm summary and writes the rows for boot.py."""
import json, sys, os, glob, math, re
DETAIL = {"then", "more", "why", "example"}
LOOK = {"read_screen", "run_cli", "investigate", "recall"}
ASKW = {"investigate", "prompt", "run_cli"}

def wilson(k, n, z=1.96):
    if n == 0: return (0, 0)
    p = k / n; d = 1 + z*z/n; c = (p + z*z/(2*n)) / d; h = z*math.sqrt(p*(1-p)/n + z*z/(4*n*n)) / d
    return (c - h, c + h)

def user_turns(ev):
    order, tt = [], {}
    for l in open(ev):
        o = json.loads(l); p = o.get("payload", {})
        if o["type"] == "brain.turn_started" and p.get("trigger") == "ceo":
            order.append(p["turn_id"]); tt[p["turn_id"]] = {"calls": [], "dur": None, "fb": False, "unchanged": 0}
        elif o["type"] == "brain.tool_call" and p.get("turn_id") in tt:
            tt[p["turn_id"]]["calls"].append(p["name"] + ":" + str((p.get("args") or {}).get("source", "")))
        elif o["type"] == "brain.turn" and p.get("turn_id") in tt:
            tt[p["turn_id"]]["dur"] = p.get("dur_ms"); tt[p["turn_id"]]["fb"] = bool(p.get("fallback"))
        elif o["type"] == "brain.read_unchanged" and p.get("turn_id") in tt:
            tt[p["turn_id"]]["unchanged"] += 1
    return [tt[i] for i in order]

def main():
    specs = {s["id"]: s for s in json.load(open(sys.argv[1]))["scenarios"] if s.get("explain")}
    fj = {}
    for l in open(sys.argv[2]):
        o = json.loads(l); fj[(os.path.normpath(o["dir"]), o["scenario"], o["repeat"])] = {a.get("line"): a for a in o["verdict"].get("answers", [])}
    rows, ctl, first, yes_rows = [], [], [], []
    for rd in sys.argv[4:]:
        rep = json.load(open(os.path.join(rd, "report.json")))
        for run in rep["runs"]:
            sid, r = run["scenario"], run["repeat"]
            if sid not in specs: continue
            kinds = specs[sid]["explain"]["turns"]
            ev = os.path.join(rd, "events", f"{sid}.r{r}.jsonl")
            ts = user_turns(ev) if os.path.exists(ev) else []
            ans = fj.get((os.path.normpath(rd), sid, r))
            ceo = [t for t in run["transcript"] if t["who"] == "ceo"]
            k = -1; cur = None
            for i, line in enumerate(ceo):
                turn = ts[i] if i < len(ts) else {"calls": [], "dur": None, "fb": False, "unchanged": 0}
                a = (ans or {}).get(i + 1, {})
                if line.get("tag") == "answer":
                    yes_rows.append(dict(dir=rd, sid=sid, rep=r, calls=turn["calls"], dur=turn["dur"], fb=turn["fb"]))
                    if cur is not None:
                        cur["yes"] = True; cur["yes_calls"] = turn["calls"]; cur["yes_dur"] = turn["dur"]
                        cur["new_ep"] += a.get("new_facts", 0); cur["wrong_ep"] = cur["wrong_ep"] or bool(a.get("wrong"))
                        cur["quotes"] += a.get("wrong", [])
                        cur["asked"] = cur["asked"] or any(c.split(":")[0] in ASKW for c in turn["calls"])
                    continue
                k += 1; kind = kinds[k] if k < len(kinds) else "?"
                base = dict(dir=rd, sid=sid, rep=r, i=k, kind=kind, judged=ans is not None and bool(a), calls=turn["calls"], dur=turn["dur"], fb=turn["fb"],
                            unchanged=turn["unchanged"], look=any(c.split(":")[0] in LOOK for c in turn["calls"]),
                            wrong=bool(a.get("wrong")), quotes=list(a.get("wrong", [])), new=a.get("new_facts", 0), restates=bool(a.get("restates")),
                            offer=bool(a.get("offers_to_ask")), screen_left=a.get("screen_left", 0) or 0,
                            asked=any(c.split(":")[0] in ASKW for c in turn["calls"]), yes=False)
                base["new_ep"], base["wrong_ep"] = base["new"], base["wrong"]
                cur = None
                if kind in DETAIL: rows.append(base); cur = base
                elif kind == "simple": ctl.append(base)
                elif kind == "explain": first.append(base); cur = base
    json.dump(rows, open(sys.argv[3], "w"), ensure_ascii=False)
    det = [r for r in rows if r["judged"]]
    def rate(rs, f):
        n = len(rs); x = sum(1 for r in rs if f(r)); lo, hi = wilson(x, n); return f"{x}/{n} = {x/max(1,n):.0%} ({lo:.0%}-{hi:.0%})"
    def mean(rs, f): return sum(f(r) for r in rs) / max(1, len(rs))
    print(f"detail lines: {len(rows)} (judged {len(det)}); runs {len(set((r['dir'], r['sid'], r['rep']) for r in rows))}")
    print("  PRIMARY restating follow-ups   ", rate(det, lambda r: r["restates"]))
    print("  new facts per detail episode    %.2f" % mean(det, lambda r: r["new_ep"]))
    print("  new facts on the detail line    %.2f" % mean(det, lambda r: r["new"]))
    print("  wrong fact in the episode      ", rate(det, lambda r: r["wrong_ep"]))
    print("  failed (iter6 composite, line) ", rate(det, lambda r: r["wrong"] or (r["restates"] and r["new"] == 0)))
    print("  no fresh look                  ", rate(rows, lambda r: not r["look"]))
    print("  read marked unchanged (A)      ", rate(rows, lambda r: r["unchanged"] > 0))
    print("  offers to ask (judge)          ", rate(det, lambda r: r["offer"]))
    print("  offers with screen facts left (noise)", rate([r for r in det if r["offer"]], lambda r: r["screen_left"] > 0))
    print("  offers answered yes (sim)      ", rate(rows, lambda r: r["yes"]))
    print("  episodes that asked (investigate/prompt/run_cli on line or yes)", rate(rows, lambda r: r["asked"]))
    calls = {}
    for r in rows + yes_rows:
        for c in r["calls"] + r.get("yes_calls", []) if "yes_calls" in r else r["calls"]:
            calls[c] = calls.get(c, 0) + 1
    inv = sum(1 for r in rows for c in r["calls"] + r.get("yes_calls", []) if c.startswith("investigate"))
    pro = sum(1 for r in rows for c in r["calls"] + r.get("yes_calls", []) if c.startswith("prompt"))
    print(f"  investigate starts on detail lines + their yes: {inv}; prompts to the worker: {pro}")
    print(f"  yes lines: {len(yes_rows)}; their calls: " + ", ".join(sorted(set(c for y in yes_rows for c in y['calls']))))
    d = sorted(r["dur"] for r in rows if r["dur"] is not None)
    if d: print("  detail-turn dur_ms median %d p90 %d max %d; over 8 s %d/%d; fallbacks %d" % (d[len(d)//2], d[int(len(d)*.9)], d[-1], sum(x > 8000 for x in d), len(d), sum(r["fb"] for r in rows)))
    y = sorted(r["dur"] for r in yes_rows if r["dur"] is not None)
    if y: print("  yes-turn dur_ms median %d p90 %d max %d; fallbacks %d" % (y[len(y)//2], y[int(len(y)*.9)], y[-1], sum(r["fb"] for r in yes_rows)))
    print("  first answers wrong", rate([r for r in first if r["judged"]], lambda r: r["wrong"]), "mean new %.2f" % mean(first, lambda r: r["new"]))
    print("  controls: reads", sum(1 for r in ctl for c in r["calls"] if c.split(":")[0] in LOOK), "on", len(ctl), "turns; wrong", rate([r for r in ctl if r["judged"]], lambda r: r["wrong"]), "; offers", sum(r["offer"] for r in ctl))
    if os.environ.get("ROWS"):
        for r in rows:
            if r["quotes"]: print("   ", r["sid"], r["rep"], r["kind"], r["quotes"])
main()
