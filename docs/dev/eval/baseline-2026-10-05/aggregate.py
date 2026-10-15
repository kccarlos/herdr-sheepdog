#!/usr/bin/env python3
"""Aggregate tier-2 bench report dirs into the numbers of baseline-2026-10-05.md.

usage: aggregate.py LABEL=dir[,dir...] [LABEL=dir,...]
Each LABEL is one arm; the dirs are merged (a top-up pass is a second dir).
Runs with status error / not_run are not counted (same as the bench)."""
import glob, json, os, statistics, sys, collections


def pct(a, b):
    return f"{a}/{b} ({100*a/b:.0f}%)" if b else "0/0"


def q(xs, p):
    xs = sorted(xs)
    if not xs:
        return None
    k = (len(xs) - 1) * p
    f = int(k)
    c = min(f + 1, len(xs) - 1)
    return xs[f] + (xs[c] - xs[f]) * (k - f)


def load(dirs):
    runs = []
    for d in dirs:
        for rp in sorted(glob.glob(os.path.join(d, "runs", "*.json"))):
            r = json.load(open(rp))
            ep = os.path.join(d, "events", os.path.basename(rp)[:-5] + ".jsonl")
            r["_events"] = ep
            r["_dir"] = d
            runs.append(r)
    return runs


def events(r):
    out = []
    if os.path.exists(r["_events"]):
        for l in open(r["_events"]):
            e = json.loads(l)
            if e.get("at_ms", -1) >= 0:
                out.append(e)
    return out


def summarise(label, dirs):
    runs = [r for r in load(dirs) if r["status"] in ("pass", "fail")]
    n = len(runs)
    p = sum(r["status"] == "pass" for r in runs)
    print(f"\n## {label}: {pct(p, n)}")
    cat = collections.OrderedDict()
    sc = collections.OrderedDict()
    for r in sorted(runs, key=lambda r: (r["category"], r["scenario"])):
        cat.setdefault(r["category"], [0, 0]); sc.setdefault(r["scenario"], [0, 0, r["category"]])
        cat[r["category"]][1] += 1; sc[r["scenario"]][1] += 1
        if r["status"] == "pass":
            cat[r["category"]][0] += 1; sc[r["scenario"]][0] += 1
    print("### by category")
    for c, (a, b) in cat.items():
        print(f"| {c} | {pct(a, b)} |")
    print("### by scenario")
    for s, (a, b, c) in sc.items():
        print(f"| {s} | {c} | {a}/{b} |")
    prim = collections.Counter(); allf = collections.Counter()
    for r in runs:
        if r["status"] == "fail":
            fs = r["failures"] or []
            prim[fs[0]["type"] if fs else "?"] += 1
            for f in fs:
                allf[f["type"]] += 1
    print("### failure primary labels", dict(prim))
    print("### failure labels (all)", dict(allf))
    # latency
    tot, fa = [], []
    for r in runs:
        for l in r.get("latency") or []:
            if l.get("total_ms") is not None:
                tot.append(l["total_ms"])
            if l.get("first_audio_ms") is not None:
                fa.append(l["first_audio_ms"])
    print(f"### turn.latency total p50 {q(tot,.5)/1000:.2f}s p90 {q(tot,.9)/1000:.2f}s (n={len(tot)}); first_audio p50 {q(fa,.5)/1000:.2f}s p90 {q(fa,.9)/1000:.2f}s")
    # audio timing
    ans = gaps = 0; gl = []; ratios = []
    for r in runs:
        for e in events(r):
            if e["type"] == "voice.audio_timing":
                pl = e["payload"]
                if pl.get("audio_ms", 0) >= 2000:
                    ans += 1
                    ratios.append(pl.get("realtime_ratio", 0))
                    if pl.get("max_gap_ms", 0) >= 3000:
                        gaps += 1; gl.append(round(pl["max_gap_ms"] / 1000, 1))
    print(f"### answers>=2s {ans}, gaps>=3s {gaps} ({100*gaps/max(ans,1):.1f}%) sizes {gl}; median realtime_ratio {statistics.median(ratios) if ratios else None}")
    # voice layer
    wer = collections.defaultdict(list)
    heardnone = 0; lines = 0
    for r in runs:
        for ul in r.get("user_lines") or []:
            lines += 1
            if ul.get("heard_none") or ul.get("heard") == "":
                heardnone += 1
            if ul.get("wer") is not None:
                wer[r.get("language") or "all"].append(ul["wer"])
    print(f"### CEO lines {lines}, voice heard none {heardnone}")
    # resume
    for r in runs:
        if r["scenario"].startswith("resume-recall"):
            ev = events(r)
            rc = [e["payload"] for e in ev if e["type"] == "voice.resume_context"]
            hd = [e["payload"].get("reason") for e in ev if e["type"] == "voice.handle_dropped"]
            rs = [e["payload"] for e in ev if e["type"] == "voice.reseeded"]
            print(f"### {r['scenario']} {os.path.basename(r['_dir'])} {r['status']}: resumes " + ", ".join(f"gen{x['generation']} {x['before_prompt']}->{x['after_prompt']} ({x['after_prompt']/x['before_prompt']:.2f})" for x in rc if x.get('before_prompt') and x.get('after_prompt')) + f"; handle_dropped {hd}; reseeded {[(x['items'],x['chars']) for x in rs]}")
    # failovers / fallbacks
    fb = sum(1 for r in runs for e in events(r) if e["type"] == "brain.fallback")
    print("### brain.fallback events", fb)


if __name__ == "__main__":
    for a in sys.argv[1:]:
        label, ds = a.split("=", 1)
        summarise(label, ds.split(","))
