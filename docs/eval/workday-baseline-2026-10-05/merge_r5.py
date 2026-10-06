#!/usr/bin/env python3
"""Merge the tier-1 top-up runs into the tier-1 campaign.

The tier-1 campaign (--repeat 5 --personas rotate) hit its budget after 29
of 35 runs; the six missing fifth repeats were run one by one afterwards
(--repeat 1 --personas <the persona rotate gives repeat 5>), each into
TOPUP/<scenario>/. This writes OUT/report.json (the campaign's graded
runs plus the top-ups renumbered r5) and OUT/events/ (the event logs,
the top-ups renamed .r5.jsonl), the input of `bench northstar` and
aggregate.py.

usage: merge_r5.py T1_DIR TOPUP_DIR OUT_DIR
"""
import glob, json, os, shutil, sys

t1, topup, out = sys.argv[1:4]
rep = json.load(open(os.path.join(t1, "report.json")))
runs = [r for r in rep["runs"] if r["status"] != "not_run"]
os.makedirs(os.path.join(out, "events"), exist_ok=True)
for p in glob.glob(os.path.join(t1, "events", "*.jsonl")):
    shutil.copy(p, os.path.join(out, "events", os.path.basename(p)))
for d in sorted(glob.glob(os.path.join(topup, "workday-*"))):
    sub = json.load(open(os.path.join(d, "report.json")))
    for r in sub["runs"]:
        if r["status"] == "not_run":
            continue
        src = os.path.join(d, "events", f"{r['scenario']}.r{r['repeat']}.jsonl")
        r["repeat"] = 5
        runs.append(r)
        if os.path.exists(src):
            shutil.copy(src, os.path.join(out, "events", f"{r['scenario']}.r5.jsonl"))
rep["runs"] = sorted(runs, key=lambda r: (r["scenario"], r["repeat"]))
rep.pop("north_star", None)
json.dump(rep, open(os.path.join(out, "report.json"), "w"), indent=1, ensure_ascii=False)
print(len(rep["runs"]), "runs")
