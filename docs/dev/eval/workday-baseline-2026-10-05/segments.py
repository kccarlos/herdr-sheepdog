#!/usr/bin/env python3
"""Segment-level Understood per scenario and persona (2026-10-06).

usage: segments.py T1_REPORT_JSON T2_RUNS_DIR

Reads the segment verdicts that `sheepdog bench rejudge` added and prints,
per tier, segment Understood (a request segment whose `answered` and
`length_fits` are yes) by scenario and by persona, and the share of runs
where every request segment is understood. Plain shares, no CI (the
north-star block has the cluster-bootstrap CIs)."""
import collections, glob, json, os, re, sys

DEPTH = re.compile(r"^(explain_depth|explain_padding)|length|depth")


def understood(seg):
    if seg["kind"] != "request":
        return None
    ans = [v["answer"] for v in seg["verdicts"] if v["name"] == "answered"]
    if not ans or ans[0] not in ("yes", "no"):
        return None
    return ans[0] == "yes" and all(v["answer"] == "yes" for v in seg["verdicts"] if DEPTH.search(v["name"]))


def table(name, runs):
    by = {"scenario": collections.defaultdict(lambda: [0, 0, 0, 0]), "persona": collections.defaultdict(lambda: [0, 0, 0, 0])}
    for r in runs:
        if r.get("status") not in ("pass", "fail") or not r.get("segments"):
            continue
        us = [u for u in map(understood, r["segments"]) if u is not None]
        for key, val in (("scenario", r["scenario"] + (" (held out)" if r.get("held_out") else "")), ("persona", r.get("persona") or "-")):
            c = by[key][val]
            c[0] += sum(us)
            c[1] += len(us)
            c[2] += all(us)
            c[3] += 1
    for key in ("scenario", "persona"):
        print(f"\n{name}, by {key}\n\n| {key} | segments understood | runs with every segment understood |\n|---|---|---|")
        for val, (k, n, a, m) in sorted(by[key].items()):
            print(f"| {val} | {k}/{n} ({100 * k / max(n, 1):.0f} %) | {a}/{m} |")


t1 = json.load(open(sys.argv[1]))["runs"]
t2 = [json.load(open(p)) for p in sorted(glob.glob(os.path.join(sys.argv[2], "*.json")))]
table("Tier 1", t1)
table("Tier 2", t2)
